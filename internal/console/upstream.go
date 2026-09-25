package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"stepstash/internal/cacheproxy"
)

var errSongRemoved = errors.New("歌曲已离开待预缓存队列")

// Auto starts with HKG and retries CF once. Resolve each route independently:
// never rewrite a returned URL, whose content version may differ by upstream.
func (c *Console) prefetchSong(ctx context.Context, engine *cacheproxy.Server, id int64, wanted func() bool) (string, error) {
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
		slog.Info("prefetch_upstream", "song_id", id, "upstream", route, "fallback", attempt > 0)
		target, err := c.resolveNode(ctx, id, route)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if wanted != nil && !wanted() {
			return "", errSongRemoved
		}
		if err == nil {
			var source string
			source, err = engine.Prefetch(ctx, target)
			if err == nil {
				return source, nil
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failures = append(failures, fmt.Errorf("%s：%w", route, err))
		slog.Warn("prefetch_upstream_failed", "song_id", id, "upstream", route, "error", err)
	}
	return "", errors.Join(failures...)
}
