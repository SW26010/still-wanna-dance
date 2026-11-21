package upstreamrequest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// ResourceScheme is the execution protocol for validated video addresses.
// Original API-returned URLs remain evidence and are not rewritten in storage.
const ResourceScheme = "http"

// ErrHeaderBudget indicates that another candidate was given its turn before
// this route's own timeout. It is a scheduling decision, not a route failure.
var ErrHeaderBudget = fmt.Errorf("response header budget exhausted: %w", context.DeadlineExceeded)

// Attempts bounds candidate execution across response headers and body failures.
// It never selects or changes a request's protocol.
type Attempts struct {
	// RetrySingle preserves one body retry when only one path is configured.
	RetrySingle bool
	count, used int
}

func NewAttempts(count int) *Attempts {
	return &Attempts{count: count}
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
// the full timeout for the request and body. When another route is available,
// fallbackDeadline can supply the latest source estimate for reserving its turn.
// remaining includes this candidate. The caller owns cancellation through close.
func NewAttempt(ctx context.Context, source *http.Client, timeout time.Duration, remaining int, fallbackDeadline func() time.Time) (context.Context, *http.Client, context.CancelFunc) {
	bounded, stop := context.WithTimeout(ctx, timeout)
	client := *source
	if remaining == 1 {
		// No other route needs a share. A second timer at the parent's deadline
		// would race it and could hide the actual cancellation cause.
		return bounded, &client, stop
	}
	budget := timeout
	var cause error = context.DeadlineExceeded
	var estimate time.Time
	if fallbackDeadline != nil {
		estimate = fallbackDeadline()
	}
	now := time.Now()
	deadline, _ := ctx.Deadline()
	if estimate.After(now) && (deadline.IsZero() || estimate.Before(deadline)) {
		deadline = estimate
	}
	if !deadline.IsZero() {
		if share := deadline.Sub(now) / time.Duration(remaining); share < budget {
			budget, cause = share, ErrHeaderBudget
		}
	}
	child, cancel := context.WithCancelCause(bounded)
	timer := time.AfterFunc(budget, func() { cancel(cause) })
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
		child, client, cancel := NewAttempt(ctx, source, timeout, len(clients)-index, nil)
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
