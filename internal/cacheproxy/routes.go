package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
)

func (s *Server) routeRequest(ctx context.Context, v video) (*http.Request, error) {
	u := &url.URL{Scheme: s.cfg.OriginScheme, Host: v.host, Path: v.path, RawQuery: v.query}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err == nil {
		r.Host = v.host
		r.Header.Set("Accept-Encoding", "identity")
	}
	return r, err
}

// Follow only validated video redirects. Rebuild every request through the
// origin transport, upgrading HTTP Locations without ever sending plaintext.
// API requests retain the client's no-redirect policy so their Location can be
// parsed separately.
func (s *Server) videoResponse(r *http.Request, original video, attempts *resourceAttempts) (*http.Response, error) {
	for redirects := 0; redirects <= 5; redirects++ {
		resp, err := s.resourceResponse(r, attempts)
		if err != nil {
			return nil, err
		}
		if !IsSongRedirect(resp.StatusCode) && resp.StatusCode != http.StatusSeeOther {
			return resp, nil
		}
		resp.Body.Close()
		u, err := resp.Location()
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return nil, errors.New("invalid video redirect")
		}
		next, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		v, err := s.parse(next)
		// The task's validated URL remains trusted even when the live API
		// registry has expired. This does not grant inbound host membership.
		if err != nil || (v.host != original.host && !s.resourceHostAllowed(v.host)) || v.key != original.key || v.size != original.size {
			return nil, errors.New("video redirect changed resource or host")
		}
		next, err = s.routeRequest(r.Context(), v)
		if err != nil {
			return nil, err
		}
		next.Header = r.Header.Clone()
		r = next
	}
	return nil, errors.New("too many video redirects")
}

// Retry measured channels of the same address only. The loading caller owns
// alternate URLs and API lookup; download workers never probe or rank routes.
type resourceCandidates struct {
	transports []http.RoundTripper
	next       int
}
type resourceAttempts struct {
	targets map[string]*resourceCandidates
}

func (s *Server) candidates(r *http.Request, attempts *resourceAttempts) *resourceCandidates {
	if attempts.targets == nil {
		attempts.targets = make(map[string]*resourceCandidates)
	}
	key := r.URL.String()
	if candidates := attempts.targets[key]; candidates != nil {
		return candidates
	}
	var transports []http.RoundTripper
	if s.cfg.ResourceTransports != nil {
		transports = s.cfg.ResourceTransports(key)
	}
	if len(transports) == 0 {
		transports = []http.RoundTripper{s.client.Transport}
	}
	candidates := &resourceCandidates{transports: append([]http.RoundTripper(nil), transports[:min(4, len(transports))]...)}
	attempts.targets[key] = candidates
	return candidates
}

// The cursor survives header validation and body reads, including the first
// response handed to the streaming worker. It is private to one range, not a
// route cache or health ranking. Close owns cancellation of a successful body.
type resourceBody struct {
	io.ReadCloser
	cancel   context.CancelFunc
	ctx      context.Context
	budget   *time.Timer
	attempts *resourceAttempts
	once     sync.Once
	err      error
}

func (b *resourceBody) Close() error {
	b.once.Do(func() { b.useTaskDeadline(); b.cancel(); b.err = b.ReadCloser.Close() })
	return b.err
}

func (b *resourceBody) useTaskDeadline() {
	if b.budget != nil {
		b.budget.Stop()
	}
}

func (b *resourceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && b.ctx.Err() != nil {
		err = context.Cause(b.ctx)
	}
	return n, err
}

func (s *Server) resourceResponse(r *http.Request, attempts *resourceAttempts) (*http.Response, error) {
	candidates := s.candidates(r, attempts)
	transports := candidates.transports
	var failures []error
	// One configured channel may retry a failed block once. Multiple measured
	// channels advance without revisiting a failed candidate (at most four).
	limit := max(2, len(transports))
	for candidates.next < limit {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		i := candidates.next
		candidates.next++
		transport := transports[i%len(transports)]
		if transport == nil {
			continue
		}
		client := *s.client
		client.Transport = transport
		ctx, cancelCause := context.WithCancelCause(r.Context())
		cancel := func() { cancelCause(context.Canceled) }
		var budget *time.Timer
		if deadline, ok := r.Context().Deadline(); ok && len(transports) > 1 && limit-i > 1 {
			// Use a removable timer, not an immutable child deadline: a valid
			// full-body 200 must be able to retain the parent task's deadline.
			budget = time.AfterFunc(time.Until(deadline)/time.Duration(limit-i), func() { cancelCause(context.DeadlineExceeded) })
		}
		resp, err := client.Do(r.Clone(ctx))
		if err == nil && resp.StatusCode < 500 {
			resp.Body = &resourceBody{ReadCloser: resp.Body, cancel: cancel, ctx: ctx, budget: budget, attempts: attempts}
			return resp, nil
		}
		if err == nil {
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
			resp.Body.Close()
		}
		if err != nil && ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		if budget != nil {
			budget.Stop()
		}
		cancel()
		failures = append(failures, applog.SafeError(err))
		if len(transports) == 1 {
			break
		}
	}
	if len(failures) == 0 {
		return nil, errors.New("no executable resource channel")
	}
	return nil, errors.Join(failures...)
}

func (s *Server) openUpstream(ctx context.Context, v video) (*http.Response, string, error) {
	r, err := s.routeRequest(ctx, v)
	if err != nil {
		return nil, "", err
	}
	r.Header.Set("Range", fmt.Sprintf("bytes=0-%d", min(v.size, rangeBlockSize)-1))
	attempts := new(resourceAttempts)
	for {
		resp, err := s.videoResponse(r, v, attempts)
		if err != nil {
			return nil, "", applog.SafeError(err)
		}
		switch {
		case resp.StatusCode == http.StatusPartialContent:
			err = validateRangeResponse(resp, 0, min(v.size, rangeBlockSize), v.size)
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
		case resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity":
			err = errors.New("unexpected upstream encoding")
		case resp.ContentLength >= 0 && resp.ContentLength != v.size:
			err = errors.New("upstream content length mismatch")
		}
		if err != nil {
			resp.Body.Close()
			// Retry this validated endpoint on the next measured channel.
			if resp.Request != nil {
				r = resp.Request.Clone(ctx)
			}
			candidates := s.candidates(r, attempts)
			if candidates.next < len(candidates.transports) {
				continue
			}
			return nil, "", err
		}
		if resp.StatusCode == http.StatusOK {
			if body, ok := resp.Body.(*resourceBody); ok {
				body.useTaskDeadline()
			}
		}
		return resp, v.host, nil
	}
}
