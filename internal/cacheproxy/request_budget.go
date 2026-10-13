package cacheproxy

import (
	"context"
	"slices"
	"sync"
)

// These are video HTTP operations, not flight slots or idle TCP connections.
// Keep the old default engine's six-request ceiling as Range parallelism grows;
// two slots are unavailable to speculative work, including prefetch first GETs.
const videoRequestLimit = 6
const backgroundRequestLimit = 4

// A queued block can become urgent without canceling or duplicating its request.
// The callback returns a snapshot and its change notification. It must never
// acquire the budget mutex; the budget does not run callbacks holding Server.mu.
type requestPriority func() (urgent bool, changed <-chan struct{})
type requestPriorityKey struct{}

type requestWaiter struct {
	ctx      context.Context
	priority requestPriority
}

type requestBudget struct {
	mu                  sync.Mutex
	limit, background   int
	active, speculative int
	waiters             []*requestWaiter
	changed             chan struct{}
}

func newRequestBudget(limit, background int) *requestBudget {
	return &requestBudget{limit: limit, background: background, changed: make(chan struct{})}
}

func (b *requestBudget) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (w *requestWaiter) urgent() (bool, <-chan struct{}) {
	if w.priority != nil {
		return w.priority()
	}
	return false, nil
}

// acquire uses FIFO within each class. Demand can overtake queued background
// work, but never borrows its reserved capacity to speculative requests.
// Ownership ends only after the HTTP operation and Body.Close have returned.
func (b *requestBudget) acquire(ctx context.Context) (func(), error) {
	priority, _ := ctx.Value(requestPriorityKey{}).(requestPriority)
	w := &requestWaiter{ctx: ctx, priority: priority}
	b.mu.Lock()
	b.waiters = append(b.waiters, w)
	b.signalLocked()
	for {
		index := slices.Index(b.waiters, w)
		if err := ctx.Err(); err != nil {
			b.waiters = slices.Delete(b.waiters, index, index+1)
			b.signalLocked()
			b.mu.Unlock()
			return nil, err
		}
		urgent, priorityChanged := w.urgent()
		ready := b.active < b.limit && (urgent || b.speculative < b.background)
		for i, other := range b.waiters {
			if other.ctx.Err() != nil {
				continue
			}
			otherUrgent, _ := other.urgent()
			if other == w {
				// Later urgent requests also precede this background waiter.
				if urgent {
					break
				}
				continue
			}
			if otherUrgent || !urgent && i < index {
				ready = false
				break
			}
		}
		if ready {
			b.waiters = slices.Delete(b.waiters, index, index+1)
			b.active++
			if !urgent {
				b.speculative++
			}
			b.signalLocked()
			b.mu.Unlock()
			return sync.OnceFunc(func() {
				b.mu.Lock()
				b.active--
				if !urgent {
					b.speculative--
				}
				b.signalLocked()
				b.mu.Unlock()
			}), nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		case <-priorityChanged:
		}
		b.mu.Lock()
	}
}

func (s *Server) resourceRequestContext(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.resourceRankLocked(key).level == 0, s.capacityChanged
	}))
}
