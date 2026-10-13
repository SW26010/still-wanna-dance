package mihomo

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// One slot covers a native dial, its delivered stream, AND eventual Close.
// No queue: a native call ignoring cancellation keeps its slot until it exits.
// Each exit owns a separate budget so a broken exit cannot starve its siblings.
type admission struct {
	mu                       sync.Mutex
	ctx                      context.Context
	cancel                   context.CancelFunc
	closed                   bool
	limit, occupied, workers int
	timeout                  time.Duration
	connections              map[*admittedConn]struct{}
	drained                  chan struct{}
}

type admittedConn struct {
	net.Conn
	owner *admission
	once  sync.Once
}

func newAdmission(limit int, timeout time.Duration) *admission {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	return &admission{ctx: ctx, cancel: cancel, limit: limit, timeout: timeout, connections: make(map[*admittedConn]struct{}), drained: done}
}

// Native Close may itself block. Only one cleanup goroutine per occupied slot
// is allowed; callers and retirement never wait unboundedly inside native code.
// Close initiates cleanup, while Runtime.Close reports whether it drained.
func (c *admittedConn) Close() error {
	c.once.Do(func() {
		go func() {
			_ = c.Conn.Close()
			c.owner.mu.Lock()
			delete(c.owner.connections, c)
			c.owner.occupied--
			c.owner.signalDrained()
			c.owner.mu.Unlock()
		}()
	})
	return nil
}

func (a *admission) signalDrained() {
	if a.occupied == 0 && a.workers == 0 {
		close(a.drained)
	}
}

func (a *admission) wrap(dial upstreamrequest.DialFunc) upstreamrequest.DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		host, port, err := net.SplitHostPort(address)
		n, pe := strconv.ParseUint(port, 10, 16)
		if err != nil || pe != nil || n == 0 || host == "" || (network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, ErrDial
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, upstreamrequest.ErrRetired
		}
		if a.occupied >= a.limit {
			a.mu.Unlock()
			return nil, upstreamrequest.ErrCapacity
		}
		if a.occupied == 0 && a.workers == 0 {
			a.drained = make(chan struct{})
		}
		a.occupied++
		a.workers++
		a.mu.Unlock()
		op, cancel := context.WithTimeout(ctx, a.timeout)
		defer cancel()
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
		type result struct {
			conn *admittedConn
			err  error
		}
		done := make(chan result)
		go func() {
			defer func() {
				a.mu.Lock()
				a.workers--
				a.signalDrained()
				a.mu.Unlock()
			}()
			conn, err := dial(op, network, address)
			if op.Err() != nil {
				err = op.Err()
			}
			if a.ctx.Err() != nil {
				err = upstreamrequest.ErrRetired
			}
			if err == nil && conn == nil {
				err = ErrDial
			}
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, upstreamrequest.ErrRetired) {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					err = context.DeadlineExceeded
				} else {
					err = ErrDial
				}
			}
			var owned *admittedConn
			a.mu.Lock()
			if conn != nil {
				owned = &admittedConn{Conn: conn, owner: a}
				a.connections[owned] = struct{}{}
			} else {
				a.occupied--
			}
			a.mu.Unlock()
			if err != nil && owned != nil {
				_ = owned.Close()
				owned = nil
			}
			select {
			case done <- result{owned, err}:
			case <-op.Done():
				if owned != nil {
					_ = owned.Close()
				}
			}
		}()
		select {
		case r := <-done:
			// A successful handoff can race cancellation or runtime retirement.
			err := ctx.Err()
			if err == nil && a.ctx.Err() != nil {
				err = upstreamrequest.ErrRetired
			}
			if err == nil {
				err = op.Err()
			}
			if err != nil {
				if r.conn != nil {
					_ = r.conn.Close()
				}
				return nil, err
			}
			if r.err != nil {
				return nil, r.err
			}
			return r.conn, nil
		case <-op.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if a.ctx.Err() != nil {
				return nil, upstreamrequest.ErrRetired
			}
			return nil, op.Err()
		}
	}
}

func (a *admission) retire() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	a.cancel()
	for c := range a.connections {
		_ = c.Close()
	}
	return a.drained
}
