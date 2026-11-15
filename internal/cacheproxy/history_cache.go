package cacheproxy

import (
	"context"
	"errors"
	"sync"
)

// Keep one recent result. Concurrent callers share work; canceled or failed
// computations are never cached. A manual refresh joins any computation in flight.
type historyCache[T any] struct {
	mu         sync.Mutex
	value      T
	valid      bool
	generation uint64
	building   *historyBuild
}

func (c *historyCache[T]) get(ctx context.Context, refresh bool, accepts func(T) bool, read func() (T, error)) (T, error) {
	var zero T
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if c.valid && accepts(c.value) && (!refresh || c.generation != generation) {
			value := c.value
			c.mu.Unlock()
			return value, nil
		}
		if b := c.building; b != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-b.done:
				if b.err != nil && !errors.Is(b.err, context.Canceled) && !errors.Is(b.err, context.DeadlineExceeded) {
					return zero, b.err
				}
			}
			continue
		}
		b := &historyBuild{done: make(chan struct{})}
		c.building = b
		c.mu.Unlock()
		value, err := read()
		if err == nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		if err == nil {
			c.value, c.valid = value, true
			c.generation++
		}
		b.err, c.building = err, nil
		close(b.done)
		c.mu.Unlock()
		return value, err
	}
}
