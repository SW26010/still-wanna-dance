package mihomo

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
)

// wireDialer owns physical sockets, including QUIC sockets which do not belong
// to any single returned stream. Registration and retirement share one lock.
type wireDialer struct {
	dns    *serverDNS
	mu     sync.Mutex
	closed bool
	owned  map[io.Closer]struct{}
}

func newWireDialer(dns *serverDNS) *wireDialer {
	return &wireDialer{dns: dns, owned: make(map[io.Closer]struct{})}
}
func (w *wireDialer) register(c io.Closer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	w.owned[c] = struct{}{}
	return true
}
func (w *wireDialer) forget(c io.Closer) { w.mu.Lock(); delete(w.owned, c); w.mu.Unlock() }
func (w *wireDialer) available() bool    { w.mu.Lock(); defer w.mu.Unlock(); return !w.closed }
func (w *wireDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !w.available() {
		return nil, net.ErrClosed
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, ErrDial
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrDial
	}
	ips, err := w.dns.lookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if network == "tcp4" && !ip.Is4() || network == "tcp6" && !ip.Is6() {
			continue
		}
		if !w.available() {
			return nil, net.ErrClosed
		}
		c, e := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // Other IPs of the same server, never another node/direct target.
		}
		owned := &wireConn{Conn: c, owner: w}
		if !w.register(owned) || ctx.Err() != nil {
			owned.Close()
			return nil, net.ErrClosed
		}
		if a, ok := ctx.Value(attemptKey{}).(*dialAttempt); ok && !a.add(owned) {
			owned.Close()
			return nil, net.ErrClosed
		}
		return owned, nil
	}
	return nil, ErrDial
}
func (w *wireDialer) ListenPacket(ctx context.Context, network, address string, remote netip.AddrPort) (net.PacketConn, error) {
	if !w.available() {
		return nil, net.ErrClosed
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if network != "udp" || address != "" || !remote.IsValid() {
		return nil, ErrDial
	}
	family := "udp6"
	if remote.Addr().Is4() {
		family = "udp4"
	}
	p, err := (&net.ListenConfig{}).ListenPacket(ctx, family, "")
	if err != nil {
		return nil, ErrDial
	}
	owned := &wirePacket{PacketConn: p, owner: w}
	if !w.register(owned) || ctx.Err() != nil {
		owned.Close()
		return nil, net.ErrClosed
	}
	if a, ok := ctx.Value(attemptKey{}).(*dialAttempt); ok && !a.add(owned) {
		owned.Close()
		return nil, net.ErrClosed
	}
	return owned, nil
}
func (w *wireDialer) Close() {
	w.mu.Lock()
	w.closed = true
	cs := make([]io.Closer, 0, len(w.owned))
	for c := range w.owned {
		cs = append(cs, c)
	}
	w.mu.Unlock()
	for _, c := range cs {
		_ = c.Close()
	}
}

type wireConn struct {
	net.Conn
	owner *wireDialer
	once  sync.Once
	err   error
}

func (c *wireConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close(); c.owner.forget(c) })
	return c.err
}

type wirePacket struct {
	net.PacketConn
	owner *wireDialer
	once  sync.Once
	err   error
}

func (c *wirePacket) Close() error {
	c.once.Do(func() { c.err = c.PacketConn.Close(); c.owner.forget(c) })
	return c.err
}

type attemptKey struct{}

// A failed/canceled handshake closes only sockets it created, never unrelated
// established sessions. Successful dials transfer them to node ownership.
type dialAttempt struct {
	mu                 sync.Mutex
	finished, canceled bool
	sockets            []io.Closer
	done               chan struct{}
}

func (a *dialAttempt) add(c io.Closer) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return false
	}
	a.sockets = append(a.sockets, c)
	return true
}
func (a *dialAttempt) commit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.canceled {
		return false
	}
	a.finished = true
	a.sockets = nil
	close(a.done)
	return true
}
func (a *dialAttempt) abort() {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		<-a.done
		return
	}
	a.finished, a.canceled = true, true
	sockets := a.sockets
	a.sockets = nil
	a.mu.Unlock()
	for _, s := range sockets {
		_ = s.Close()
	}
	close(a.done)
}
