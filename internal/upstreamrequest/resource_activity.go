package upstreamrequest

import (
	"context"
	"sync"
)

type resourceProbe struct {
	ctx    context.Context
	cancel context.CancelFunc
}
type resourceActivity struct {
	mu     sync.Mutex
	loads  int
	idle   chan struct{}
	probes map[*resourceProbe]struct{}
}

// BeginResourceLoad gives business traffic priority over resource measurements.
// Call before dialing; call the idempotent release after closing the response.
func (c *Channel) BeginResourceLoad() func() {
	a := &c.resources
	a.mu.Lock()
	if a.loads == 0 {
		a.idle = make(chan struct{})
	}
	a.loads++
	for p := range a.probes {
		p.cancel()
	}
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.loads--
			if a.loads == 0 {
				close(a.idle)
			}
			a.mu.Unlock()
		})
	}
}

func (c *Channel) ResourceBusy() bool {
	c.resources.mu.Lock()
	defer c.resources.mu.Unlock()
	return c.resources.loads > 0
}

// ResourceProbe waits without polling. Business arrivals cancel its context.
// Finish unregisters it and reports whether its sample was interrupted.
func (c *Channel) ResourceProbe(ctx context.Context) (context.Context, func() bool, error) {
	a := &c.resources
	for {
		a.mu.Lock()
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			return nil, nil, err
		}
		if a.loads == 0 {
			p := &resourceProbe{}
			p.ctx, p.cancel = context.WithCancel(ctx)
			if a.probes == nil {
				a.probes = make(map[*resourceProbe]struct{})
			}
			a.probes[p] = struct{}{}
			a.mu.Unlock()
			var once sync.Once
			var interrupted bool
			return p.ctx, func() bool {
				once.Do(func() {
					a.mu.Lock()
					interrupted = p.ctx.Err() != nil
					delete(a.probes, p)
					p.cancel()
					a.mu.Unlock()
				})
				return interrupted
			}, nil
		}
		idle := a.idle
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-idle:
		}
	}
}
