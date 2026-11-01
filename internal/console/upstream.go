package console

import (
	"context"
	"errors"
	"still-wanna-dance/internal/cacheproxy"
)

var errSongRemoved = errors.New("歌曲已离开待预加载队列")

func (c *Console) prefetchSong(ctx context.Context, engine *cacheproxy.Server, id int64) (string, error) {
	return engine.PrefetchDownload(ctx, id)
}
