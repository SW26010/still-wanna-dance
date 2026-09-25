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
