package mihomo

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

type fakeProxy struct {
	dial  func(context.Context, *C.Metadata) (C.Conn, error)
	close func() error
}

func (p *fakeProxy) DialContext(c context.Context, m *C.Metadata) (C.Conn, error) {
	return p.dial(c, m)
}
func (p *fakeProxy) Close() error {
	if p.close != nil {
		return p.close()
	}
	return nil
}
func wrap(c net.Conn) C.Conn {
	return outbound.NewConn(c, outbound.NewBase(outbound.BaseOption{Name: "test"}))
}
func testNode(p proxy) *Node {
	n := newNode("stable-v1-"+strings.Repeat("a", 64), Options{DialTimeout: time.Second})
	n.p = p
	return n
}
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete")
		var zero T
		return zero
	}
}

func TestCanceledDialClosesLateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	defer b.Close()
	n := testNode(&fakeProxy{dial: func(context.Context, *C.Metadata) (C.Conn, error) { cancel(); return wrap(a), nil }})
	defer n.Close()
	c, err := n.DialContext(ctx, "tcp", "target.invalid:80")
	if c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v", c, err)
	}
	b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(make([]byte, 1)); err == nil {
		t.Fatal("late connection survived")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("late connection leaked")
	}
}

func TestCloseWaitsForDialAndAllCloseCallers(t *testing.T) {
	started, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a, b := net.Pipe()
	defer b.Close()
	var closes atomic.Int32
	n := testNode(&fakeProxy{dial: func(ctx context.Context, _ *C.Metadata) (C.Conn, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return wrap(a), nil
	}, close: func() error { closes.Add(1); return nil }})
	dialDone := make(chan error, 1)
	go func() { _, err := n.DialContext(context.Background(), "tcp", "target.invalid:80"); dialDone <- err }()
	await(t, started)
	closeDone := make(chan error, 8)
	for range 8 {
		go func() { closeDone <- n.Close() }()
	}
	await(t, canceled)
	select {
	case <-closeDone:
		t.Fatal("Close returned before late dial cleanup")
	default:
	}
	if _, err := n.DialContext(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	close(release)
	if err := await(t, dialDone); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	for range 8 {
		if err := await(t, closeDone); err != nil {
			t.Fatal(err)
		}
	}
	if closes.Load() != 1 {
		t.Fatal("proxy closed more than once")
	}
}

func TestDialTimeoutAndErrorRedaction(t *testing.T) {
	n := testNode(&fakeProxy{dial: func(ctx context.Context, _ *C.Metadata) (C.Conn, error) {
		<-ctx.Done()
		return nil, errors.New("secret credential")
	}})
	n.timeout = 20 * time.Millisecond
	defer n.Close()
	if _, err := n.DialContext(context.Background(), "tcp", "target.invalid:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	n.p = &fakeProxy{dial: func(context.Context, *C.Metadata) (C.Conn, error) { return nil, errors.New("secret credential") }}
	if _, err := n.DialContext(context.Background(), "tcp", "target.invalid:80"); err != ErrDial {
		t.Fatal(err)
	}
}

func TestRepeatedConcurrentDialAndRetirement(t *testing.T) {
	var peersMu sync.Mutex
	var peers []net.Conn
	n := testNode(&fakeProxy{dial: func(context.Context, *C.Metadata) (C.Conn, error) {
		a, b := net.Pipe()
		peersMu.Lock()
		peers = append(peers, b)
		peersMu.Unlock()
		return wrap(a), nil
	}})
	defer func() {
		for _, p := range peers {
			p.Close()
		}
	}()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				c, e := n.DialContext(context.Background(), "tcp", "target.invalid:80")
				if e != nil {
					t.Error(e)
					return
				}
				c.Close()
				c.Close()
			}
		}()
	}
	wg.Wait()
	n.mu.Lock()
	count := len(n.streams)
	n.mu.Unlock()
	if count != 0 {
		t.Fatalf("%d closed connections retained", count)
	}
	c, err := n.DialContext(context.Background(), "tcp", "target.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, e := c.Read(make([]byte, 1)); readDone <- e }()
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if err := await(t, readDone); err == nil {
		t.Fatal("stream survived retirement")
	}
}

func TestRejectedConfigDoesNotDial(t *testing.T) {
	for _, key := range []string{"dialer-proxy", "interface-name", "routing-mark", "proxy-groups", "dns", "tun", "port-hopping", "handshake-timeout", "plugin", "ws-opts", "certificate", "smux"} {
		t.Run(key, func(t *testing.T) {
			p := map[string]any{"type": "ss", "server": "proxy.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "secret", key: true}
			_, _, err := checkedParameters(p)
			if err != ErrConfig {
				t.Fatal(err)
			}
		})
	}
	p := map[string]any{"type": "ss", "server": "proxy.invalid", "port": 443, "cipher": "aes-128-gcm", "password": "secret", "name": "sensitive"}
	cloned, _, err := checkedParameters(p)
	if err != nil {
		t.Fatal(err)
	}
	p["password"] = "mutated"
	if cloned["password"] != "secret" || cloned["name"] != "fixed-node" {
		t.Fatal("mutable or sensitive config escaped")
	}
}

func TestQueuedDialCancellationDoesNotEnterUpstream(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int32
	n := testNode(&fakeProxy{dial: func(context.Context, *C.Metadata) (C.Conn, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, ErrDial
	}})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); n.Close() })
	first := make(chan error, 1)
	go func() { _, e := n.DialContext(context.Background(), "tcp", "target.invalid:80"); first <- e }()
	await(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := n.DialContext(ctx, "tcp", "target.invalid:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled queued dial entered upstream")
	}
	releaseOnce.Do(func() { close(release) })
	await(t, first)
}
