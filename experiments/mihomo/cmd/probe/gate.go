package main

import (
	"context"
	"errors"
	"maps"
	"net"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
)

// Experiment-only ownership boundary. Retirement cancels pending dials and
// closes in-flight streams before closing the node's reusable sessions.
type gate struct {
	C.Proxy
	mu          sync.Mutex
	closed      bool
	life        context.Context
	cancel      context.CancelFunc
	dials       sync.WaitGroup
	connections map[*ownedConn]bool
}
type ownedConn struct {
	C.Conn
	owner *gate
	once  sync.Once
}

func (c *ownedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.connections, c); c.owner.mu.Unlock() })
	return err
}
func newGate(p C.Proxy) *gate {
	ctx, cancel := context.WithCancel(context.Background())
	return &gate{Proxy: p, life: ctx, cancel: cancel, connections: map[*ownedConn]bool{}}
}
func (g *gate) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, net.ErrClosed
	}
	g.dials.Add(1)
	g.mu.Unlock()
	defer g.dials.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.life, cancel)
	defer stop()
	defer cancel()
	if g.life.Err() != nil {
		return nil, net.ErrClosed
	}
	c, err := g.Proxy.DialContext(ctx, m)
	if ctx.Err() != nil {
		if c != nil {
			c.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		c.Close()
		return nil, net.ErrClosed
	}
	owned := &ownedConn{Conn: c, owner: g}
	g.connections[owned] = true
	return owned, nil
}
func (g *gate) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	g.cancel()
	g.mu.Unlock()
	g.dials.Wait()
	g.mu.Lock()
	var conns []*ownedConn
	for c := range g.connections {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	return g.Proxy.Close()
}

func checkGuard(node map[string]any) error {
	name := node["type"].(string) + "/guard/"
	p, err := adapter.ParseProxy(node)
	if err != nil {
		return err
	}
	g := newGate(p)
	defer g.Close()
	record(name+"http", func() (bool, string) { err := request(g, "/", time.Second); return err == nil, detail(err) })
	record(name+"pre-canceled-dial", func() (bool, string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, err := dial(g, ctx)
		if c != nil {
			c.Close()
		}
		return errors.Is(err, context.Canceled), detail(err)
	})
	c, err := dial(g, context.Background())
	if err != nil {
		return err
	}
	// Force protocol headers / session stream establishment before retirement.
	c.SetDeadline(time.Now().Add(time.Second))
	if _, err = c.Write([]byte("GET /stall HTTP/1.1\r\nHost: business.invalid\r\n\r\n")); err != nil {
		c.Close()
		return err
	}
	g.Close()
	record(name+"retire-closes-owned-stream", func() (bool, string) {
		_, err := c.Read(make([]byte, 1))
		if err == nil {
			return false, "stream remained readable"
		}
		if e, ok := err.(net.Error); ok && e.Timeout() {
			return false, "retirement did not unblock stream"
		}
		return true, detail(err)
	})
	record(name+"retired-refuses-new-dial", func() (bool, string) {
		_, err := dial(g, context.Background())
		return errors.Is(err, net.ErrClosed), detail(err)
	})
	slow := maps.Clone(node)
	slow["server"] = "cancel.invalid"
	p, err = adapter.ParseProxy(slow)
	if err != nil {
		return err
	}
	cold := newGate(p)
	defer cold.Close()
	record(name+"cold-dns-deadline", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		c, err := dial(cold, ctx)
		if c != nil {
			c.Close()
		}
		return errors.Is(err, context.DeadlineExceeded), detail(err)
	})
	return nil
}
