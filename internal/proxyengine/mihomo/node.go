// Package mihomo owns fixed Mihomo outbounds. It never loads a runtime config,
// creates an inbound, changes OS networking, or falls back to direct egress.
package mihomo

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter"
	C "github.com/metacubex/mihomo/constant"
	"still-wanna-dance/internal/upstreamrequest"
)

// Config is one immutable fixed exit, not a complete Mihomo configuration.
// ID must be the caller's keyed connection-configuration fingerprint, including
// credentials. Never reuse it for a different exit or derive it from a name.
type Config struct {
	ID         string
	Parameters map[string]any
}

// LookupIP must bypass OS hosts and honor cancellation. It is called only for
// this node's server, never for business targets. It must be concurrency safe.
type LookupIP func(context.Context, string) ([]netip.Addr, error)
type Options struct {
	LookupIP LookupIP
	// DialTimeout bounds DNS and connection establishment, not returned streams.
	// Zero uses ten seconds. Request owners must close streams on cancellation.
	DialTimeout time.Duration
}

var (
	ErrConfig = errors.New("mihomo: unsupported or invalid node configuration")
	ErrDial   = errors.New("mihomo: connection failed")
	ErrDNS    = errors.New("mihomo: server DNS unavailable")
)

type proxy interface {
	DialContext(context.Context, *C.Metadata) (C.Conn, error)
	Close() error
}

type Node struct {
	id       string
	p        proxy
	wire     *wireDialer
	dns      *serverDNS
	timeout  time.Duration
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	streams  map[*stream]struct{}
	dials    sync.WaitGroup
	setup    chan struct{}
	done     chan struct{}
	closeErr error
}

// New parses without dialing. Mihomo types and parser/dial error details never
// cross this boundary; those errors can contain credentials or node values.
func New(config Config, options Options) (*Node, error) {
	if !upstreamrequest.PersistentProxyID(config.ID) || options.LookupIP == nil || options.DialTimeout < 0 {
		return nil, ErrConfig
	}
	params, server, err := checkedParameters(config.Parameters)
	if err != nil {
		return nil, err
	}
	initializeDNS()
	n := newNode(config.ID, options)
	n.dns = &serverDNS{host: server, lookup: options.LookupIP}
	n.wire = newWireDialer(n.dns)
	p, err := adapter.ParseProxy(params, adapter.WithDialerForAPI(n.wire))
	if err != nil {
		n.cancel()
		n.wire.Close()
		return nil, ErrConfig
	}
	n.p = p
	return n, nil
}

func newNode(id string, options Options) *Node {
	ctx, cancel := context.WithCancel(context.Background())
	d := options.DialTimeout
	if d == 0 {
		d = 10 * time.Second
	}
	return &Node{id: id, timeout: d, life: ctx, cancel: cancel, streams: make(map[*stream]struct{}), setup: make(chan struct{}, 1), done: make(chan struct{})}
}

// Proxy is directly usable with upstreamrequest.NewMultiPool. Pool owns its
// HTTP transports; the caller still owns Node.Close. Retire both generations.
func (n *Node) Proxy() upstreamrequest.Proxy {
	return upstreamrequest.Proxy{ID: n.id, Dial: n.DialContext}
}

func (n *Node) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, ErrDial
	}
	host, port, err := net.SplitHostPort(address)
	p, pe := strconv.ParseUint(port, 10, 16)
	if err != nil || pe != nil || host == "" || p == 0 {
		return nil, ErrDial
	}
	m := &C.Metadata{NetWork: C.TCP, Type: C.INNER, Host: host, DstPort: uint16(p)}
	if ip, e := netip.ParseAddr(host); e == nil {
		if ip.Zone() != "" {
			return nil, ErrDial
		}
		m.DstIP, m.Host = ip.Unmap(), ""
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, net.ErrClosed
	}
	n.dials.Add(1)
	n.mu.Unlock()
	defer n.dials.Done()
	dialCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	stop := context.AfterFunc(n.life, cancel)
	defer stop()
	if n.life.Err() != nil {
		return nil, net.ErrClosed
	}
	// Hysteria2's foreground offer holds a non-context-aware mutex during
	// cold setup. Queue here so later canceled callers never block on it.
	// Only establishment is serialized; returned streams remain concurrent.
	select {
	case n.setup <- struct{}{}:
		defer func() { <-n.setup }()
	case <-dialCtx.Done():
		if n.life.Err() != nil {
			return nil, net.ErrClosed
		}
		return nil, dialCtx.Err()
	}
	if n.life.Err() != nil {
		return nil, net.ErrClosed
	}
	if dialCtx.Err() != nil {
		return nil, dialCtx.Err()
	}
	dialCtx = context.WithValue(dialCtx, dnsKey{}, n.dns)
	attempt := &dialAttempt{done: make(chan struct{})}
	dialCtx = context.WithValue(dialCtx, attemptKey{}, attempt)
	stopAttempt := context.AfterFunc(dialCtx, attempt.abort)
	defer stopAttempt()
	c, err := n.p.DialContext(dialCtx, m)
	n.mu.Lock()
	closed := n.closed
	if closed || dialCtx.Err() != nil || err != nil || c == nil || !attempt.commit() {
		n.mu.Unlock()
		attempt.abort()
		if c != nil {
			_ = c.Close()
		}
		if closed {
			return nil, net.ErrClosed
		}
		if dialCtx.Err() != nil {
			return nil, dialCtx.Err()
		}
		return nil, ErrDial
	}
	s := &stream{Conn: c, owner: n}
	n.streams[s] = struct{}{}
	n.mu.Unlock()
	return s, nil
}

// Close is terminal and concurrent/idempotent. Every caller waits for the same
// completion. Retirement immediately aborts owned streams; there is no grace
// period or session resurrection. LookupIP implementations must honor context.
func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		<-n.done
		return n.closeErr
	}
	n.closed = true
	n.cancel()
	streams := make([]*stream, 0, len(n.streams))
	for s := range n.streams {
		streams = append(streams, s)
	}
	n.mu.Unlock()
	// Physical sockets first: unblock protocol handshakes and stream Close.
	if n.wire != nil {
		n.wire.Close()
	}
	for _, s := range streams {
		_ = s.Close()
	}
	n.dials.Wait()
	if err := n.p.Close(); err != nil {
		n.closeErr = errors.New("mihomo: close failed")
	}
	close(n.done)
	return n.closeErr
}

type stream struct {
	net.Conn
	owner *Node
	once  sync.Once
	err   error
}

func (s *stream) Close() error {
	s.once.Do(func() {
		s.err = s.Conn.Close()
		s.owner.mu.Lock()
		delete(s.owner.streams, s)
		s.owner.mu.Unlock()
	})
	return s.err
}
