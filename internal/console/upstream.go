package console

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
)

var errSongRemoved = errors.New("歌曲已离开待预加载队列")

func (c *Console) prefetchSong(ctx context.Context, engine *cacheproxy.Server, id int64) (string, error) {
	ctx = applog.WithTrace(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := engine.CheckSongAllowed(ctx, id); err != nil {
		return "", err
	}
	log := slog.Default().With("trace_id", applog.TraceID(ctx), "song_id", id)
	started := time.Now()
	target, err := c.resolveSongURL(ctx, id)
	log.Info("prefetch_resolved", "elapsed_ms", time.Since(started).Milliseconds(), "success", err == nil, "error", applog.SafeError(err))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var source string
	if err == nil {
		source, err = engine.PrefetchSong(ctx, strconv.FormatInt(id, 10), target)
	}
	if err != nil {
		log.Warn("prefetch_upstream_failed", "error", applog.SafeError(err))
	}
	return source, err
}
