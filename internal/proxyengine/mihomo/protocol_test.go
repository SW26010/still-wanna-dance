package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// The fixture is built as a separate executable. No server/inbound package is
// imported into the production adapter's test process to mask initialization.
var fixtureNodes []map[string]any

func TestMain(m *testing.M) { os.Exit(runTests(m)) }
func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "mihomo-fixture-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	exe := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", exe, "./testfixture")
	if b, e := build.CombinedOutput(); e != nil {
		panic(string(b) + e.Error())
	}
	manifest := filepath.Join(dir, "manifest.json")
	server := exec.CommandContext(ctx, exe, manifest)
	log, err := os.Create(filepath.Join(dir, "fixture.log"))
	if err != nil {
		panic(err)
	}
	defer log.Close()
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		panic(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if b, e := os.ReadFile(manifest); e == nil {
			var result struct {
				Nodes []map[string]any `json:"nodes"`
			}
			if json.Unmarshal(b, &result) == nil {
				fixtureNodes = result.Nodes
				break
			}
		}
		if time.Now().After(deadline) {
			panic("fixture readiness timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m.Run()
}
func nodeOptions(t *testing.T, calls *atomic.Int32) Options {
	t.Helper()
	return Options{DialTimeout: time.Second, LookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
		calls.Add(1)
		if host != "proxy.invalid" {
			t.Errorf("unexpected DNS %q", host)
			return nil, ErrDNS
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
}
func httpThrough(tr http.RoundTripper, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "http://business.invalid"+path, nil)
	resp, err := (&http.Client{Transport: tr}).Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 || string(b) != "mihomo-embedded-business-response\n" {
		return errors.New("wrong egress response")
	}
	return nil
}
func TestProtocolsAndMultiPool(t *testing.T) {
	for _, params := range fixtureNodes {
		t.Run(params["type"].(string), func(t *testing.T) {
			t.Parallel()
			var callsA, callsB atomic.Int32
			a, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, nodeOptions(t, &callsA))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			b, err := New(Config{ID: "stable-v1-" + strings.Repeat("b", 64), Parameters: params}, nodeOptions(t, &callsB))
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			pool, err := upstreamrequest.NewMultiPool("socks5", nil, []upstreamrequest.Proxy{a.Proxy(), b.Proxy()})
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			cs := pool.Current("http://business.invalid/")
			if len(cs) != 2 {
				t.Fatal("missing fixed exits")
			}
			var wg sync.WaitGroup
			for _, c := range cs {
				for range 4 {
					wg.Add(1)
					go func(tr http.RoundTripper) {
						defer wg.Done()
						for range 3 {
							if e := httpThrough(tr, "/", 2*time.Second); e != nil {
								t.Error(e)
								return
							}
						}
					}(c.Transport)
				}
			}
			wg.Wait()
			if t.Failed() {
				return
			}
			if callsA.Load() == 0 || callsB.Load() == 0 {
				t.Fatal("node DNS not independent")
			}
			for _, n := range []*Node{a, b} {
				n.wire.mu.Lock()
				owned := len(n.wire.owned)
				n.wire.mu.Unlock()
				if owned == 0 {
					t.Fatal("live protocol sockets bypassed node ownership")
				}
			}
			if err := httpThrough(cs[0].Transport, "/stall", 30*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if c, e := a.DialContext(ctx, "tcp", "business.invalid:80"); c != nil || !errors.Is(e, context.Canceled) {
				t.Fatal("warm cancellation failed", e)
			}
			// Actual protocol stream must unblock when its node retires.
			c, err := a.DialContext(context.Background(), "tcp", "business.invalid:80")
			if err != nil {
				t.Fatal(err)
			}
			c.SetDeadline(time.Now().Add(time.Second))
			if _, err = c.Write([]byte("GET /stall HTTP/1.1\r\nHost: business.invalid\r\n\r\n")); err != nil {
				t.Fatal(err)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Read(make([]byte, 1)); err == nil {
				t.Fatal("retired stream remained open")
			} else if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("retirement failed to unblock read")
			}
			if _, err = a.DialContext(context.Background(), "tcp", "business.invalid:80"); !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
			if err = httpThrough(cs[0].Transport, "/", time.Second); err == nil {
				t.Fatal("retired exit succeeded")
			}
			if err = httpThrough(cs[1].Transport, "/", time.Second); err != nil {
				t.Fatal("sibling node disrupted", err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
			for _, n := range []*Node{a, b} {
				n.mu.Lock()
				streams := len(n.streams)
				n.mu.Unlock()
				n.wire.mu.Lock()
				sockets := len(n.wire.owned)
				n.wire.mu.Unlock()
				if streams != 0 || sockets != 0 {
					t.Fatalf("retained streams=%d sockets=%d", streams, sockets)
				}
			}
		})
	}
}

func TestConvertedVLESSTLSUsesAuditedFingerprint(t *testing.T) {
	for _, params := range fixtureNodes {
		if params["type"] != "vless" {
			continue
		}
		u := &url.URL{Scheme: "vless", User: url.User(params["uuid"].(string)), Host: net.JoinHostPort(params["server"].(string), fmt.Sprint(params["port"]))}
		query := url.Values{"security": {"tls"}, "sni": {params["servername"].(string)}, "pcs": {params["fingerprint"].(string)}}
		u.RawQuery = query.Encode()
		result, err := Import([]byte(u.String()))
		if err != nil || result.Accepted != 1 {
			t.Fatal("VLESS import failed", err)
		}
		var calls atomic.Int32
		n, err := New(Config{ID: "stable-v1-" + strings.Repeat("c", 64), Parameters: result.Nodes[0]}, nodeOptions(t, &calls))
		if err != nil {
			t.Fatal(err)
		}
		defer n.Close()
		pool, err := upstreamrequest.NewMultiPool("socks5", nil, []upstreamrequest.Proxy{n.Proxy()})
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := httpThrough(pool.Current("http://business.invalid")[0].Transport, "/", 2*time.Second); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("missing VLESS fixture")
}

func TestRealColdDNSCancellationAndRetirement(t *testing.T) {
	for _, params := range fixtureNodes {
		t.Run(params["type"].(string), func(t *testing.T) {
			entered := make(chan struct{}, 2)
			n, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, Options{LookupIP: func(ctx context.Context, _ string) ([]netip.Addr, error) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			done := make(chan error, 1)
			go func() { _, e := n.DialContext(context.Background(), "tcp", "business.invalid:80"); done <- e }()
			await(t, entered)
			if err = n.Close(); err != nil {
				t.Fatal(err)
			}
			if err = await(t, done); !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}

func TestHandshakeCancellationReleasesPhysicalSocket(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	params := map[string]any{"type": "vless", "server": "proxy.invalid", "port": listener.Addr().(*net.TCPAddr).Port, "uuid": "d342d11e-d424-4583-b36e-524ab1f0afa4", "tls": true}
	var calls atomic.Int32
	n, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, nodeOptions(t, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := n.DialContext(ctx, "tcp", "business.invalid:80"); done <- e }()
	peer := await(t, accepted)
	defer peer.Close()
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	n.wire.mu.Lock()
	count := len(n.wire.owned)
	n.wire.mu.Unlock()
	if count != 0 {
		t.Fatal("failed handshake retained physical socket")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	_, err = io.Copy(io.Discard, peer)
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("remote connection still open")
	}
}

func TestInvalidCredentialsDoNotReachBusiness(t *testing.T) {
	params := maps.Clone(fixtureNodes[0])
	params["password"] = "wrong-password"
	var calls atomic.Int32
	n, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, nodeOptions(t, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	tr := &http.Transport{DialContext: n.DialContext}
	defer tr.CloseIdleConnections()
	if err = httpThrough(tr, "/", time.Second); err == nil {
		t.Fatal("invalid credentials bypassed the fixed exit")
	}
}

func TestCanceledNodeCanDialAgain(t *testing.T) {
	for _, params := range fixtureNodes {
		t.Run(params["type"].(string), func(t *testing.T) {
			var block atomic.Bool
			block.Store(true)
			entered := make(chan struct{})
			var enteredOnce sync.Once
			n, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, Options{LookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
				if host != "proxy.invalid" {
					return nil, ErrDNS
				}
				if block.Load() {
					enteredOnce.Do(func() { close(entered) })
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := n.DialContext(ctx, "tcp", "business.invalid:80"); done <- e }()
			await(t, entered)
			cancel()
			if err = await(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			block.Store(false)
			tr := &http.Transport{DialContext: n.DialContext}
			defer tr.CloseIdleConnections()
			if err = httpThrough(tr, "/", time.Second); err != nil {
				t.Fatal("canceled node could not be reused", err)
			}
		})
	}
}

func TestQUICHandshakeTimeoutReleasesSocket(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	params := map[string]any{"type": "hysteria2", "server": "proxy.invalid", "port": listener.LocalAddr().(*net.UDPAddr).Port, "password": "test"}
	var calls atomic.Int32
	options := nodeOptions(t, &calls)
	options.DialTimeout = 50 * time.Millisecond
	n, err := New(Config{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: params}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err = n.DialContext(context.Background(), "tcp", "business.invalid:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	n.wire.mu.Lock()
	count := len(n.wire.owned)
	n.wire.mu.Unlock()
	if count != 0 {
		t.Fatal("failed QUIC handshake retained socket")
	}
}
