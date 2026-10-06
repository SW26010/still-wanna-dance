package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"still-wanna-dance/internal/applog"
)

type routeHealth struct {
	latency time.Duration
	until   time.Time
	failed  bool
}

// Unknown resource URLs never establish song ownership. Only explicit song
// requests or the library's recorded mappings can supply the API song ID.
func (s *Server) routeCandidates(ctx context.Context, v video) []video {
	if s.cfg.ResolveRoutes == nil {
		return []video{v}
	}
	id := v.songID
	s.routeMu.Lock()
	if id == "" {
		id, _ = s.routeSongs.get(v.key, time.Now())
	}
	s.routeMu.Unlock()
	if id == "" {
		_ = s.usage.db.QueryRowContext(ctx, `SELECT song_id FROM song_media WHERE md5=? ORDER BY song_id LIMIT 1`, v.key).Scan(&id)
	}
	if id == "" {
		return []video{v}
	}
	s.routeMu.Lock()
	urls, cached := s.routeCache.get(id, time.Now())
	s.routeMu.Unlock()
	if !cached {
		resolveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var err error
		urls, err = s.cfg.ResolveRoutes(resolveCtx, id)
		cancel()
		if len(urls) == 0 {
			s.cfg.Logger.Debug("routes_unavailable", "song_id", id, "error", applog.SafeError(err))
			return []video{v}
		}
		s.routeMu.Lock()
		s.routeCache.put(id, urls, time.Now().Add(time.Minute), routeCacheLimit)
		s.routeMu.Unlock()
	}
	var candidates []video
	seen := make(map[string]bool)
	for _, target := range urls {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil || r.URL.Fragment != "" {
			continue
		}
		candidate, err := s.parseResolved(r)
		if err != nil {
			continue
		}
		s.routeMu.Lock()
		s.routeSongs.put(candidate.key, id, time.Time{}, routeSongsLimit)
		s.routeMu.Unlock()
		if candidate.key != v.key || candidate.size != v.size {
			s.cfg.Logger.Warn("route_content_mismatch", "song_id", id, "host", candidate.host)
			continue
		}
		// One real URL per upstream bounds probing and download attempts.
		if !seen[candidate.host] {
			seen[candidate.host] = true
			candidates = append(candidates, candidate)
		}
	}
	if s.cfg.KeepRequestedRoute && !seen[v.host] {
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		return []video{v}
	}
	return candidates
}

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
		resp, err := s.client.Do(r)
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

func (s *Server) noteRoute(host string, elapsed time.Duration, failed bool) {
	ttl := 2 * time.Minute
	if failed {
		ttl = 15 * time.Second
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.routeHealth == nil {
		s.routeHealth = make(map[string]routeHealth)
	}
	s.routeHealth[host] = routeHealth{latency: elapsed, failed: failed, until: time.Now().Add(ttl)}
}

// Completed downloads confirm health without extending the probe lifetime.
// Otherwise a continuously used route would never be compared again.
func (s *Server) confirmRoute(host string, elapsed time.Duration) {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.routeHealth == nil {
		s.routeHealth = make(map[string]routeHealth)
	}
	h := s.routeHealth[host]
	if h.until.IsZero() || h.failed {
		s.routeHealth[host] = routeHealth{latency: elapsed, until: time.Now().Add(2 * time.Minute)}
	}
}

// Small Range probes measure time to receive bytes, not merely TCP latency.
// Ignore servers that disregard Range, without reading their whole response.
func (s *Server) probeRoute(ctx context.Context, v video) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := s.routeRequest(ctx, v)
	if err != nil {
		return
	}
	n := min(v.size, int64(64<<10))
	r.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := s.videoResponse(r, v)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", n-1, v.size) || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
			err = errors.New("invalid probe response")
		} else {
			var read int64
			read, err = io.Copy(io.Discard, io.LimitReader(resp.Body, n+1))
			if read != n {
				err = errors.New("incomplete probe")
			}
		}
	}
	if parent.Err() == nil {
		s.noteRoute(v.host, time.Since(start), err != nil)
	}
}

func (s *Server) rankRoutes(ctx context.Context, candidates []video) {
	if len(candidates) < 2 {
		return
	}
	var pending []chan struct{}
	for _, v := range candidates {
		s.routeMu.Lock()
		h := s.routeHealth[v.host]
		if time.Now().Before(h.until) {
			s.routeMu.Unlock()
			continue
		}
		if s.routeProbes == nil {
			s.routeProbes = make(map[string]chan struct{})
		}
		done, exists := s.routeProbes[v.host]
		if !exists {
			done = make(chan struct{})
			s.routeProbes[v.host] = done
		}
		pending = append(pending, done)
		s.routeMu.Unlock()
		if !exists {
			go func(v video, done chan struct{}) {
				s.probeRoute(ctx, v)
				s.routeMu.Lock()
				delete(s.routeProbes, v.host)
				close(done)
				s.routeMu.Unlock()
			}(v, done)
		}
	}
	for _, done := range pending {
		<-done
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := s.routeHealth[candidates[i].host], s.routeHealth[candidates[j].host]
		if a.failed != b.failed {
			return !a.failed
		}
		return a.latency < b.latency
	})
}

// All cache misses enter here, including streaming playback and both background
// entry points. Retry only before publication starts; never splice two bodies.
func (s *Server) openUpstream(ctx context.Context, v video) (*http.Response, string, error) {
	candidates := s.routeCandidates(ctx, v)
	if v.preferRequestedRoute {
		// Keep independently resolved, content-matched alternatives for failures,
		// but do not let playback latency samples override background policy.
		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].host == v.host && candidates[j].host != v.host
		})
	} else {
		s.rankRoutes(ctx, candidates)
	}
	var failures []error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		start := time.Now()
		r, err := s.routeRequest(ctx, candidate)
		if err != nil {
			return nil, "", err
		}
		r.Header.Set("Range", fmt.Sprintf("bytes=0-%d", min(v.size, rangeBlockSize)-1))
		resp, err := s.videoResponse(r, v)
		if err == nil {
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
			if err == nil {
				s.recordUpstream(time.Since(start))
				s.cfg.Logger.Info("upstream_selected", "requested_host", v.host, "host", candidate.host, "resource_key", v.key)
				return resp, candidate.host, nil
			}
			resp.Body.Close()
		}
		s.noteRoute(candidate.host, time.Since(start), true)
		failures = append(failures, fmt.Errorf("%s: %w", candidate.host, applog.SafeError(err)))
	}
	return nil, "", errors.Join(failures...)
}
