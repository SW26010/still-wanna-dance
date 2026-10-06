package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
func (s *Server) videoResponse(r *http.Request, original video) (*http.Response, error) {
	for redirects := 0; redirects <= 5; redirects++ {
		resp, err := s.resourceResponse(r)
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
		if err != nil || !s.resourceHostAllowed(v.host) || v.key != original.key || v.size != original.size {
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
func (s *Server) resourceResponse(r *http.Request) (*http.Response, error) {
	var transports []http.RoundTripper
	if s.cfg.ResourceTransports != nil {
		transports = s.cfg.ResourceTransports(r.URL.String())
	}
	if len(transports) == 0 {
		return s.client.Do(r)
	}
	var failures []error
	for i, transport := range transports {
		if i == 4 {
			break
		}
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if transport == nil {
			continue
		}
		client := *s.client
		client.Transport = transport
		resp, err := client.Do(r)
		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}
		if err == nil {
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
			resp.Body.Close()
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, errors.New("no executable resource channel")
	}
	return nil, errors.Join(failures...)
}

func (s *Server) openUpstream(ctx context.Context, v video) (*http.Response, string, error) {
	start := time.Now()
	r, err := s.routeRequest(ctx, v)
	if err != nil {
		return nil, "", err
	}
	r.Header.Set("Range", fmt.Sprintf("bytes=0-%d", min(v.size, rangeBlockSize)-1))
	resp, err := s.videoResponse(r, v)
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
		return nil, "", err
	}
	s.recordUpstream(time.Since(start))
	return resp, v.host, nil
}
