package cacheproxy

import (
	"context"
	"sync"
)

// VideoRequestSoftLimit is the normal scheduler capacity (Blocking can exceed it).
const VideoRequestSoftLimit = 16
const videoRequestSoftLimit = VideoRequestSoftLimit

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
