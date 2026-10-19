package cacheproxy

import (
	"context"
	"sync"
	"time"
)

// VideoRequestSoftLimit is the default ordinary capacity, also used by isolated
// throughput measurements. Playback engines may configure a different limit.
const VideoRequestSoftLimit = 16
const videoRequestSoftLimit = VideoRequestSoftLimit
const MaxDownloadSoftLimit = 64
const DefaultDownloadGrowInterval = 250 * time.Millisecond
const MaxDownloadGrowInterval = 2000 * time.Millisecond

// The block scheduler is the only admission controller. Track each actual
// HTTP attempt through Body.Close without adding a second queue or hard limit.
type requestActivity struct {
	mu     sync.Mutex
	active int
}

func (b *requestActivity) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.active++
	b.mu.Unlock()
	return sync.OnceFunc(func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}), nil
}
