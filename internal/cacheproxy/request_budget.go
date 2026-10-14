package cacheproxy

import (
	"context"
	"sync"
)

const videoRequestLimit = 6
const backgroundRequestLimit = 4

// The block scheduler owns ordering and urgency. This guard only bounds actual
// HTTP operations, including retries and redirects, until Body.Close returns.
type requestBudget struct {
	mu                     sync.Mutex
	limit, active, waiting int
	changed                chan struct{}
	slots                  chan struct{}
}

func newRequestBudget(limit int) *requestBudget {
	return &requestBudget{limit: limit, slots: make(chan struct{}, limit), changed: make(chan struct{})}
}
func (b *requestBudget) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
func (b *requestBudget) acquire(ctx context.Context) (func(), error) {
	b.mu.Lock()
	b.waiting++
	b.signalLocked()
	b.mu.Unlock()
	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		b.mu.Lock()
		b.waiting--
		b.signalLocked()
		b.mu.Unlock()
		return nil, ctx.Err()
	}
	b.mu.Lock()
	b.waiting--
	b.active++
	b.signalLocked()
	b.mu.Unlock()
	release := sync.OnceFunc(func() {
		b.mu.Lock()
		b.active--
		<-b.slots
		b.signalLocked()
		b.mu.Unlock()
	})
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
