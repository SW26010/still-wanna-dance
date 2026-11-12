package upstreamrequest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// ResourceScheme is the execution protocol for validated video addresses.
// Original API-returned URLs remain evidence and are not rewritten in storage.
const ResourceScheme = "http"

// Attempts bounds candidate execution across response headers and body failures.
// It never selects or changes a request's protocol.
type Attempts struct {
	// RetrySingle preserves one body retry when only one path is configured.
	RetrySingle bool
	count, used int
}

func NewAttempts(count int) *Attempts {
	return &Attempts{count: min(count, MaxOperationCandidates)}
}

func (a *Attempts) Next() (int, bool) {
	if a.used < a.count {
		index := a.used
		a.used++
		return index, true
	}
	if a.RetrySingle && a.count == 1 && a.used == 1 {
		a.used++
		return 0, true
	}
	return 0, false
}

// AttemptError preserves the deadline/cancellation that closed a response body,
// without replacing independent content, certificate or local storage errors.
func AttemptError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		var ne net.Error
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) ||
			errors.As(err, &ne) && ne.Timeout() {
			return context.Cause(ctx)
		}
	}
	return err
}

// NewAttempt gives headers a share of the remaining deadline while preserving
// the full timeout for the request and body. headerDeadline optionally supplies
// an earlier scheduling deadline that is not enforced by ctx; its owner still
// controls cancellation. remaining includes the current candidate and must be
// positive. The caller owns cancellation through response body close.
func NewAttempt(ctx context.Context, source *http.Client, timeout time.Duration, remaining int, headerDeadline time.Time) (context.Context, *http.Client, context.CancelFunc) {
	budget := timeout
	deadline, _ := ctx.Deadline()
	if !headerDeadline.IsZero() && (deadline.IsZero() || headerDeadline.Before(deadline)) {
		deadline = headerDeadline
	}
	if !deadline.IsZero() {
		budget = min(budget, time.Until(deadline)/time.Duration(remaining))
	}
	bounded, stop := context.WithTimeout(ctx, timeout)
	child, cancel := context.WithCancelCause(bounded)
	timer := time.AfterFunc(budget, func() { cancel(context.DeadlineExceeded) })
	client := *source
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = headerBudgetTransport{RoundTripper: transport, stop: timer.Stop}
	return child, &client, func() {
		timer.Stop()
		cancel(nil)
		stop()
	}
}

// Fetch performs complete, validated reads in candidate order with a fixed URL.
// Header waits share the caller's deadline; a progressing body retains the full
// attempt/caller deadline. No preference survives an operation.
func Fetch[T any](ctx context.Context, target string, clients []*http.Client, timeout time.Duration, read func(context.Context, *http.Client, string) (T, error)) (T, error) {
	var zero T
	var failures []error
	clients = clients[:min(len(clients), MaxOperationCandidates)]
	for index, source := range clients {
		if ctx.Err() != nil {
			return zero, context.Cause(ctx)
		}
		child, client, cancel := NewAttempt(ctx, source, timeout, len(clients)-index, time.Time{})
		value, err := read(child, client, target)
		err = AttemptError(child, err)
		cancel()
		if err == nil {
			return value, nil
		}
		failures = append(failures, err)
	}
	if ctx.Err() != nil {
		return zero, context.Cause(ctx)
	}
	if len(failures) == 0 {
		return zero, ErrUnavailable
	}
	return zero, errors.Join(failures...)
}

// Only bounds waiting for headers. Keep channel provenance available to callers.
type headerBudgetTransport struct {
	http.RoundTripper
	stop func() bool
}

func (t headerBudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(r)
	t.stop()
	return resp, err
}
func (t headerBudgetTransport) ChannelID() string {
	if channel, ok := t.RoundTripper.(interface{ ChannelID() string }); ok {
		return channel.ChannelID()
	}
	return ""
}

// Do returns redirects to their business owner; it never changes protocols.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c.Do(req)
}
