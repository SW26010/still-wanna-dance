package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/upstreamrequest"
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
		// This redirect succeeded. A pause before the next request must keep
		// its validated destination rather than rewind the previous channel.
		attempts.current, attempts.request = nil, next
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
	current *resourceCandidates
	request *http.Request
}

// Local scheduling cancellation is not a failed channel. Resume the last
// validated request on that channel without revisiting earlier failures.
func (a *resourceAttempts) resume() *http.Request {
	if a.current != nil {
		a.current.next--
		a.current = nil
	}
	return a.request
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
	candidates := &resourceCandidates{transports: append([]http.RoundTripper(nil), transports[:min(upstreamrequest.MaxOperationCandidates, len(transports))]...)}
	attempts.targets[key] = candidates
	return candidates
}

// The cursor survives header validation and body reads, including the first
// response handed to the streaming worker. It is private to one range, not a
// route cache or health ranking. Close owns cancellation of a successful body.
type resourceBody struct {
	io.ReadCloser
	cancel    context.CancelFunc
	ctx       context.Context
	stopBody  func() bool
	closeBody func() error
	attempts  *resourceAttempts
	once      sync.Once
	err       error
	release   func()
}

func (b *resourceBody) Close() error {
	b.once.Do(func() {
		if b.release != nil {
			defer b.release()
		}
		if b.stopBody != nil {
			b.stopBody()
		}
		b.cancel()
		b.err = b.closeBody()
	})
	return b.err
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
	// channels advance without revisiting a failed candidate. Each attempt has its
	// own timeout; a shorter caller/task deadline still bounds the whole operation.
	limit := max(2, len(transports))
	for candidates.next < limit {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		release, err := s.requests.acquire(r.Context())
		if err != nil {
			return nil, err
		}
		i := candidates.next
		candidates.next++
		transport := transports[i%len(transports)]
		if transport == nil {
			release()
			continue
		}
		client := *s.client
		client.Transport = transport
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DownloadTimeout)
		attempts.current, attempts.request = candidates, r
		if s.cfg.BeginResourceLoad != nil {
			endLoad, endRequest := s.cfg.BeginResourceLoad(), release
			release = sync.OnceFunc(func() { endLoad(); endRequest() })
		}
		resp, err := client.Do(r.Clone(ctx))
		if err == nil && resp.StatusCode < 500 {
			closeBody := sync.OnceValue(resp.Body.Close)
			stopBody := context.AfterFunc(ctx, func() { closeBody() })
			resp.Body = &resourceBody{ReadCloser: resp.Body, cancel: cancel, ctx: ctx, stopBody: stopBody, closeBody: closeBody, attempts: attempts, release: release}
			return resp, nil
		}
		if err == nil {
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
			resp.Body.Close()
		}
		if err != nil && ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		if !errors.Is(context.Cause(ctx), errRangeYielded) && !errors.Is(context.Cause(ctx), errPreempted) && !errors.Is(context.Cause(ctx), blocksched.ErrPaused) {
			// A real failure consumes this candidate. A later Seek may cancel
			// the next candidate before headers; resume must not rewind to the
			// earlier failed attempt in that case.
			attempts.current = nil
		}
		cancel()
		release()
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

func (s *Server) openUpstream(ctx context.Context, v video, block int64, attempts *resourceAttempts) (*http.Response, string, error) {
	r, err := s.routeRequest(ctx, v)
	if err != nil {
		return nil, "", err
	}
	if attempts.request != nil {
		r = attempts.request.Clone(ctx)
	}
	start := block * rangeBlockSize
	end := min(v.size, start+rangeBlockSize)
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	for {
		resp, err := s.videoResponse(r, v, attempts)
		if err != nil {
			return nil, "", applog.SafeError(err)
		}
		switch {
		case resp.StatusCode == http.StatusPartialContent:
			err = validateRangeResponse(resp, start, end, v.size)
		case resp.StatusCode != http.StatusOK:
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
		case resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity":
			err = errors.New("unexpected upstream encoding")
		case resp.ContentLength >= 0 && resp.ContentLength != v.size:
			err = errors.New("upstream content length mismatch")
		}
		if err != nil {
			resp.Body.Close()
			attempts.current = nil // Rejected headers consume this channel.
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
		return resp, v.host, nil
	}
}
