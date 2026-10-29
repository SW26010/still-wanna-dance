package upstreamrequest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// Attempts owns one endpoint's bounded request cursor, including body failures.
// Callers validate content and report the complete attempt's error to Next.
// HTTPS paths precede HTTP fallbacks; a fallback consumes the same six-attempt
// budget. Source URLs, candidate identities and connection adapters stay intact.
type Attempts struct {
	// RetrySingle preserves the resource loader's one retry after a failed body
	// on a sole configured path. It does not increase the shared attempt ceiling.
	RetrySingle       bool
	count, next, used int
	scheme            string
	last              Attempt
	fallbacks         []int
	blocked           bool
	upgrade           bool
}

type Attempt struct {
	Index  int
	Scheme string
}

func NewAttempts(scheme string, count int) *Attempts {
	return &Attempts{scheme: scheme, count: min(count, MaxOperationCandidates)}
}

func (a *Attempts) Next(err error) (Attempt, bool) {
	var upgrade *httpsUpgrade
	if errors.As(err, &upgrade) {
		a.RequireHTTPS()
	}
	if a.used > 0 {
		if !HTTPFallbackAllowed(err) {
			a.blocked = true
		} else if a.last.Scheme == "https" {
			a.fallbacks = append(a.fallbacks, a.last.Index)
		}
	}
	if a.used >= MaxOperationCandidates || a.count == 0 {
		return Attempt{}, false
	}
	// Reserve the last slot for a qualified fallback instead of multiplying the
	// budget by the number of protocols. Known business/certificate failures do
	// not qualify, so all remaining HTTPS candidates can still be attempted.
	reserve := a.scheme == "https" && !a.blocked && len(a.fallbacks) > 0 && a.used == MaxOperationCandidates-1
	var next Attempt
	switch {
	case a.upgrade:
		next = Attempt{Index: a.last.Index, Scheme: "https"}
		a.upgrade = false
	case a.next < a.count && !reserve:
		next = Attempt{Index: a.next, Scheme: a.scheme}
		a.next++
	case !a.blocked && len(a.fallbacks) > 0:
		next = Attempt{Index: a.fallbacks[0], Scheme: "http"}
		a.fallbacks = a.fallbacks[1:]
		// No return to the HTTPS phase after starting plaintext fallbacks.
		a.next = a.count
	case a.RetrySingle && a.count == 1 && a.used == 1:
		next = a.last
	default:
		return Attempt{}, false
	}
	a.used++
	a.last = next
	return next, true
}

// RequireHTTPS permits an explicit same-endpoint upgrade on the last path. It
// consumes a slot and forbids further plaintext attempts in this execution.
func (a *Attempts) RequireHTTPS() {
	if a.used > 0 && a.last.Scheme == "http" {
		a.upgrade, a.blocked = true, true
	}
}

func (a *Attempts) remaining() int {
	// Reserve time for known remaining paths/fallbacks, not hypothetical HTTP
	// attempts. In particular, a healthy sole HTTPS catalog keeps its full read
	// timeout instead of losing half to a fallback it may never need.
	return max(1, min(MaxOperationCandidates-a.used+1, a.count-a.next+1+len(a.fallbacks)))
}

// HTTPFallbackAllowed classifies transport failures only. Application errors,
// certificate failures and local lifecycle/capacity errors never authorize a
// downgrade. Callers separately check the parent context before another attempt.
func HTTPFallbackAllowed(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrCapacity) || errors.Is(err, ErrRetired) || errors.Is(err, ErrUnavailable) {
		return false
	}
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &verification) || errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) {
		return false
	}
	var op *net.OpError
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &op) || errors.As(err, &ne) && ne.Timeout()
}

// AttemptError restores a timed-out request's cause after cancellation closes
// its socket/body. It must not replace certificate or local execution errors
// that happened to coincide with the deadline.
func AttemptError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && (HTTPFallbackAllowed(err) || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe)) {
		return context.Cause(ctx)
	}
	return err
}

// Fetch runs complete, validated reads over the caller's ranked clients. It has
// no health cache or background work. The parent deadline covers every attempt;
// header waiting time is shared with remaining candidates. Once headers arrive,
// the body retains the original attempt/caller deadline, so a progressing large
// catalog is not repeatedly truncated to reserve unused paths. Resource streaming
// uses Attempts directly so its cursor survives
// response headers and partial body reads.
func Fetch[T any](ctx context.Context, target string, clients []*http.Client, timeout time.Duration, read func(context.Context, *http.Client, string) (T, error)) (T, error) {
	var zero T
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return zero, errors.New("invalid upstream URL")
	}
	attempts := NewAttempts(u.Scheme, len(clients))
	var failures []error
	for {
		if ctx.Err() != nil {
			return zero, context.Cause(ctx)
		}
		attempt, ok := attempts.Next(err)
		if !ok {
			break
		}
		budget := timeout
		if deadline, ok := ctx.Deadline(); ok {
			budget = min(budget, time.Until(deadline)/time.Duration(attempts.remaining()))
		}
		bounded, stop := context.WithTimeout(ctx, timeout)
		child, cancel := context.WithCancelCause(bounded)
		timer := time.AfterFunc(budget, func() { cancel(context.DeadlineExceeded) })
		client := *clients[attempt.Index]
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		client.Transport = headerBudgetTransport{RoundTripper: transport, stop: timer.Stop}
		v := *u
		v.Scheme = attempt.Scheme
		value, failure := read(child, &client, v.String())
		failure = AttemptError(child, failure)
		timer.Stop()
		cancel(nil)
		stop()
		if failure == nil {
			return value, nil
		}
		err = failure
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return zero, ErrUnavailable
	}
	return zero, errors.Join(failures...)
}

// Only bounds waiting for headers; it neither retries nor changes the request.
// Keep channel provenance available to playback address recording.
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

type httpsUpgrade struct{}

func (*httpsUpgrade) Error() string { return "upstream endpoint requires HTTPS" }

// Do reports an HTTP endpoint's upgrade to the identical HTTPS endpoint to the
// attempt cursor. The follow-up request consumes an ordinary attempt slot. API
// video Locations remain business results; other redirects belong to callers.
func Do(client *http.Client, req *http.Request) (*http.Response, error) {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil || req.URL.Scheme != "http" || resp.StatusCode != 301 && resp.StatusCode != 302 && resp.StatusCode != 303 && resp.StatusCode != 307 && resp.StatusCode != 308 {
		return resp, err
	}
	next, err := resp.Location()
	if err != nil {
		return resp, nil
	}
	want := *req.URL
	want.Scheme = "https"
	if next.String() != want.String() {
		return resp, nil
	}
	resp.Body.Close()
	return nil, &httpsUpgrade{}
}
