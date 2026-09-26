package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"stepstash/internal/applog"
	"stepstash/internal/cacheproxy"
)

var errSongRemoved = errors.New("歌曲已离开待预缓存队列")

// Resolve the preferred route first and return immediately on success. Auto
// bounds each attempt so a stalled API still leaves time for the other route.
func (c *Console) resolvePlayback(ctx context.Context, id, node, mode string) (string, error) {
	songID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || songID <= 0 {
		return "", fmt.Errorf("invalid song ID: %s", id)
	}
	routes := []string{"hkg", "cf"}
	if node == "cf" {
		routes = []string{"cf", "hkg"}
	}
	if mode == "cf" || mode == "hkg" {
		routes = []string{mode}
	}
	var failures []error
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attemptCtx := ctx
		cancel := func() {}
		if len(routes) > 1 {
			attemptCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
		}
		target, err := c.resolveNode(attemptCtx, songID, route)
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == nil {
			return target, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", route, err))
	}
	return "", errors.Join(failures...)
}

// Resolve both routes independently; their paths and query strings may differ.
// The cache engine compares content metadata before selecting a replacement.
func (c *Console) resolveRoutes(ctx context.Context, id, mode string) ([]string, error) {
	songID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || songID <= 0 {
		return nil, fmt.Errorf("invalid song ID: %s", id)
	}
	routes := []string{"hkg", "cf"}
	if mode == "cf" || mode == "hkg" {
		routes = []string{mode}
	}
	type result struct {
		index  int
		target string
		err    error
	}
	results := make(chan result, len(routes))
	for i, route := range routes {
		go func(i int, route string) {
			target, err := c.resolveNode(ctx, songID, route)
			results <- result{i, target, err}
		}(i, route)
	}
	ordered := make([]string, len(routes))
	var failures []error
	for range routes {
		r := <-results
		if r.err != nil {
			failures = append(failures, r.err)
		} else {
			ordered[r.index] = r.target
		}
	}
	var urls []string
	for _, target := range ordered {
		if target != "" {
			urls = append(urls, target)
		}
	}
	return urls, errors.Join(failures...)
}

// Auto starts with HKG and retries CF once. Resolve each route independently:
// never rewrite a returned URL, whose content version may differ by upstream.
func (c *Console) prefetchSong(ctx context.Context, engine *cacheproxy.Server, id int64, wanted func() bool) (string, error) {
	ctx = applog.WithTrace(ctx)
	log := slog.Default().With("trace_id", applog.TraceID(ctx), "song_id", id)
	c.mu.Lock()
	mode := c.settings.DownloadUpstream
	c.mu.Unlock()
	routes := []string{"hkg", "cf"}
	if mode == "hkg" || mode == "cf" {
		routes = []string{mode}
	}
	var failures []error
	for attempt, route := range routes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if wanted != nil && !wanted() {
			return "", errSongRemoved
		}
		log.Info("prefetch_upstream", "upstream", route, "fallback", attempt > 0)
		started := time.Now()
		target, err := c.resolveNode(ctx, id, route)
		log.Info("prefetch_resolved", "upstream", route, "elapsed_ms", time.Since(started).Milliseconds(), "success", err == nil, "error", applog.SafeError(err))
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if wanted != nil && !wanted() {
			return "", errSongRemoved
		}
		if err == nil {
			var source string
			source, err = engine.PrefetchSong(ctx, strconv.FormatInt(id, 10), target)
			if err == nil {
				return source, nil
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failures = append(failures, fmt.Errorf("%s：%w", route, err))
		log.Warn("prefetch_upstream_failed", "upstream", route, "error", applog.SafeError(err))
	}
	return "", errors.Join(failures...)
}
