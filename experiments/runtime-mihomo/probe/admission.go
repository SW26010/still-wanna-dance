package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Experimental, protocol-independent ownership. A slot covers a pending dial
// OR its returned connection. Canceled callers never create a queue of workers.
// An upstream dial ignoring cancellation keeps its slot until it really ends.
type admission struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	closed              bool
	slots               chan struct{}
	work                sync.WaitGroup
	connections         map[*admittedConn]bool
	pending, peak, late int
	timeout             time.Duration
}

type admittedConn struct {
	net.Conn
	owner *admission
	once  sync.Once
}

func newAdmission(limit int, timeout time.Duration) *admission {
	ctx, cancel := context.WithCancel(context.Background())
	return &admission{ctx: ctx, cancel: cancel, slots: make(chan struct{}, limit), connections: map[*admittedConn]bool{}, timeout: timeout}
}

func (c *admittedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		<-c.owner.slots
		c.owner.mu.Unlock()
	})
	return err
}

func (a *admission) wrap(dial upstreamrequest.DialFunc) upstreamrequest.DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		host, port, err := net.SplitHostPort(address)
		n, e := strconv.Atoi(port)
		if err != nil || e != nil || host == "" || n < 1 || n > 65535 || network != "tcp" {
			return nil, errors.New("invalid-target")
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return nil, upstreamrequest.ErrUnavailable
		}
		select {
		case a.slots <- struct{}{}:
		default:
			a.mu.Unlock()
			return nil, errors.New("dial-capacity")
		}
		a.pending++
		if len(a.slots) > a.peak {
			a.peak = len(a.slots)
		}
		a.work.Add(1)
		a.mu.Unlock()
		opCtx, cancel := context.WithTimeout(ctx, a.timeout)
		stop := context.AfterFunc(a.ctx, cancel)
		type result struct {
			conn *admittedConn
			err  error
		}
		done := make(chan result)
		go func() {
			defer a.work.Done()
			defer cancel()
			defer stop()
			conn, err := dial(opCtx, network, address)
			a.mu.Lock()
			a.pending--
			if opCtx.Err() != nil {
				err = opCtx.Err()
			}
			if a.closed {
				err = upstreamrequest.ErrUnavailable
			}
			var owned *admittedConn
			if err == nil && conn == nil {
				err = errors.New("empty-connection")
			}
			if err == nil {
				owned = &admittedConn{Conn: conn, owner: a}
				a.connections[owned] = true
			} else {
				<-a.slots
			}
			a.mu.Unlock()
			if err != nil && conn != nil {
				conn.Close()
				a.mu.Lock()
				a.late++
				a.mu.Unlock()
			}
			select {
			case done <- result{owned, err}:
			case <-opCtx.Done():
				if owned != nil {
					owned.Close()
					a.mu.Lock()
					a.late++
					a.mu.Unlock()
				}
			}
		}()
		select {
		case r := <-done:
			// Cancellation can race a successful handoff.
			if err := ctx.Err(); err != nil {
				if r.conn != nil {
					r.conn.Close()
				}
				return nil, err
			}
			if a.ctx.Err() != nil {
				if r.conn != nil {
					r.conn.Close()
				}
				return nil, upstreamrequest.ErrUnavailable
			}
			if r.err != nil {
				return nil, r.err
			}
			return r.conn, nil
		case <-opCtx.Done():
			return nil, opCtx.Err()
		}
	}
}

func (a *admission) retire() {
	a.mu.Lock()
	a.closed = true
	a.cancel()
	cs := make([]*admittedConn, 0, len(a.connections))
	for c := range a.connections {
		cs = append(cs, c)
	}
	a.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

func (a *admission) wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { a.work.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (a *admission) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{"pending": a.pending, "openConnections": len(a.connections), "occupied": len(a.slots), "peak": a.peak, "limit": cap(a.slots), "lateClosed": a.late, "retired": a.closed, "dialTimeoutMS": a.timeout.Milliseconds()}
}
