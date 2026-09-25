package cacheproxy

import (
	"context"
	"net/http"
)

// Prefetch uses exactly the same validation, shared downloads and publication as playback.
// Canceling the waiter leaves an already shared download running for other clients.
func (s *Server) Prefetch(ctx context.Context, target string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	v, err := s.parse(r)
	if err != nil {
		return "", err
	}
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
