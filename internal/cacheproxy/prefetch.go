package cacheproxy

import (
	"context"
	"net/http"
	"time"
)

// Prefetch uses exactly the same validation, shared downloads and publication as playback.
// Canceling the waiter leaves an already shared download running for other clients.
func (s *Server) Prefetch(ctx context.Context, target string) (source string, resultErr error) {
	if !s.beginRequest() {
		return "", context.Canceled
	}
	defer s.wg.Done()
	start := time.Now()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	v, err := s.parse(r)
	if err != nil {
		return "", err
	}
	defer func() {
		outcome := "completed"
		if resultErr != nil {
			outcome = "failed"
		}
		if ctx.Err() != nil {
			outcome = "canceled"
		}
		cache := source
		if cache == "" {
			cache = "UNKNOWN"
		}
		s.usage.record(usageEvent{id: v.id, at: start.UnixMilli(), key: v.key, host: v.host,
			source: "prefetch", method: "GET", size: v.size, cache: cache,
			outcome: outcome, elapsedMS: time.Since(start).Milliseconds()})
	}()
	f, reader, err := s.obtain(ctx, v)
	if reader != nil {
		defer reader.Close()
	}
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.done:
		return f.source, f.err
	}
}
