package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
)

var errSongRemoved = errors.New("歌曲已离开待预缓存队列")

type batchFallbackBudgetError struct{ cause error }

func (e *batchFallbackBudgetError) Error() string {
	return e.cause.Error() + "；回退受容量预算限制"
}
func (e *batchFallbackBudgetError) Unwrap() []error {
	return []error{e.cause, cacheproxy.ErrBatchBudget}
}

func preserveBudgetFailure(previous, err error) error {
	if previous != nil && errors.Is(err, cacheproxy.ErrBatchBudget) {
		var fallback *batchFallbackBudgetError
		if errors.As(err, &fallback) {
			previous = errors.Join(previous, fallback.cause)
		}
		return &batchFallbackBudgetError{cause: previous}
	}
	return err
}

// Resolve Auto routes in monitor order, with bounded sequential fallback.
func (c *Console) resolvePlayback(ctx context.Context, id, node, mode string) (string, error) {
	songID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || songID <= 0 {
		return "", fmt.Errorf("invalid song ID: %s", id)
	}
	var failures []error
	routes := c.playbackRoutes(node, mode)
	for i, route := range routes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		budget := 10 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			budget = min(budget, time.Until(deadline)/time.Duration(len(routes)-i))
		}
		attempt, cancel := context.WithTimeout(ctx, budget)
		target, err := c.resolveNode(attempt, songID, route)
		cancel()
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
	seen := make(map[string]bool)
	for _, target := range ordered {
		if target != "" && !seen[target] {
			seen[target] = true
			urls = append(urls, target)
		}
	}
	return urls, errors.Join(failures...)
}

// Auto queries node=cf first and retries node=nya once. These select API
// requests, not resource domains. Preserve each returned URL and its version.
func (c *Console) prefetchSong(ctx context.Context, engine *cacheproxy.Server, id int64, wanted func() bool) (string, error) {
	ctx = applog.WithTrace(ctx)
	log := slog.Default().With("trace_id", applog.TraceID(ctx), "song_id", id)
	c.mu.Lock()
	mode := c.settings.DownloadUpstream
	c.mu.Unlock()
	routes := c.playbackRoutes("cf", mode)
	var failures []error
	attemptedURLs := make(map[string]bool)
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
			// Different playback nodes may return the same resource URL.
			// It is one resource endpoint, not another download fallback.
			if attemptedURLs[target] {
				continue
			}
			attemptedURLs[target] = true
			var source string
			source, err = engine.PrefetchSong(ctx, strconv.FormatInt(id, 10), target)
			if errors.Is(err, cacheproxy.ErrBatchBudget) {
				return "", preserveBudgetFailure(errors.Join(failures...), err)
			}
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
