//go:build !external_only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/component/proxydialer"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener/inner"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
	"still-wanna-dance/internal/upstreamrequest"
)

var initialized atomic.Bool

type fullRuntime struct {
	C.Tunnel
	admission        *admission
	bindings         map[string]C.Proxy
	mu               sync.Mutex
	closed           bool
	connections      map[*ownedConn]bool
	slots            chan struct{}
	work             sync.WaitGroup
	pending          atomic.Int64
	peak             atomic.Int64
	proxies          []upstreamrequest.Proxy
	coreProxies      []C.Proxy
	defaultResolver  *net.Resolver
	defaultTransport http.RoundTripper
}
type ownedConn struct {
	net.Conn
	owner *fullRuntime
	once  sync.Once
}

func (c *ownedConn) Close() error {
	var err error
	c.once.Do(func() { err = c.Conn.Close(); c.owner.mu.Lock(); delete(c.owner.connections, c); c.owner.mu.Unlock() })
	return err
}

func prepare(payload string) ([]map[string]any, error) { return nativeNodes([]byte(payload)) }

func startRuntime(init command) (*fullRuntime, error) {
	nodes, err := nativeNodes([]byte(init.Payload))
	if err != nil {
		return nil, err
	}
	if !initialized.CompareAndSwap(false, true) {
		return nil, errors.New("one-runtime-per-process")
	}
	C.SetHomeDir(init.Home)
	log.SetLevel(log.SILENT)
	cfg := map[string]any{
		"mode": "rule", "log-level": "silent", "allow-lan": false, "find-process-mode": "off",
		"port": 0, "socks-port": 0, "mixed-port": 0, "redir-port": 0, "tproxy-port": 0,
		"tun": map[string]any{"enable": false}, "sniffer": map[string]any{"enable": false}, "ntp": map[string]any{"enable": false},
		"geo-auto-update": false, "profile": map[string]any{"store-selected": false, "store-fake-ip": false},
		"dns":     map[string]any{"enable": true, "listen": "", "ipv6": false, "use-hosts": false, "use-system-hosts": false, "enhanced-mode": "redir-host", "default-nameserver": []string{init.DNS}, "nameserver": []string{init.DNS}, "proxy-server-nameserver": []string{init.DNS}},
		"proxies": nodes, "rules": []string{"MATCH,REJECT"},
	}
	data, _ := json.Marshal(cfg)
	parsed, err := executor.ParseWithBytes(data)
	if err != nil {
		return nil, errors.New("native-config-rejected")
	}
	// Same complete executor as hub.Parse, deliberately no external controller.
	savedResolver, savedTransport := net.DefaultResolver, http.DefaultTransport
	executor.ApplyConfig(parsed, true)
	r := &fullRuntime{Tunnel: tunnel.Tunnel, connections: map[*ownedConn]bool{}, slots: make(chan struct{}, 64), defaultResolver: savedResolver, defaultTransport: savedTransport}
	if init.Mode == "B2" {
		r.admission = newAdmission(64, 3*time.Second)
	}
	r.bindings = make(map[string]C.Proxy)
	for i := range nodes {
		name := fmt.Sprintf("n%d", i)
		p, ok := tunnel.Proxies()[name]
		if !ok {
			r.Close()
			return nil, errors.New("missing-fixed-outbound")
		}
		r.coreProxies = append(r.coreProxies, p)
		r.bindings[name] = p
		var dial upstreamrequest.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
			return r.dial(ctx, network, address, name)
		}
		if r.admission != nil {
			dial = r.admission.wrap(proxydialer.New(p, true).DialContext)
		}
		r.proxies = append(r.proxies, upstreamrequest.Proxy{ID: fmt.Sprintf("stable-v1-%064x", i+1), Dial: dial})
	}
	return r, nil
}

// Admission is attached to the exported Tunnel call, so canceled HTTP requests
// cannot create unbounded independent core handshakes. No protocol interception.
func (r *fullRuntime) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	defer r.work.Done()
	defer func() { <-r.slots; r.pending.Add(-1) }()
	r.Tunnel.HandleTCPConn(conn, metadata)
}
func (r *fullRuntime) dial(ctx context.Context, network, address, name string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || host == "" || n < 1 || n > 65535 || network != "tcp" {
		return nil, errors.New("invalid-target")
	}
	select {
	case r.slots <- struct{}{}:
	default:
		// net/http can detach a dial from its originating request's cancellation.
		// Do not queue detached dials behind core handshakes: bound work at entry.
		return nil, errors.New("core-entry-capacity")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || ctx.Err() != nil {
		<-r.slots
		return nil, upstreamrequest.ErrUnavailable
	}
	r.work.Add(1)
	now := r.pending.Add(1)
	for old := r.peak.Load(); now > old && !r.peak.CompareAndSwap(old, now); old = r.peak.Load() {
	}
	conn, err := inner.HandleTcp(r, address, name)
	if err != nil {
		r.work.Done()
		<-r.slots
		r.pending.Add(-1)
		return nil, errors.New("inner-entry-failed")
	}
	c := &ownedConn{Conn: conn, owner: r}
	r.connections[c] = true
	return c, nil
}
func (r *fullRuntime) ShutdownOnly() { executor.Shutdown() }

// Excludes built-ins and dynamic groups. Production bindings use New(proxy,true)
// with the captured object, so later global name changes cannot rebind them.
type fixedTunnel struct {
	C.Tunnel
	bindings map[string]C.Proxy
}

func (t fixedTunnel) Proxies() map[string]C.Proxy { return t.bindings }
func (r *fullRuntime) bindingChecks() map[string]any {
	t := fixedTunnel{Tunnel: r.Tunnel, bindings: r.bindings}
	failed := true
	for _, name := range []string{"", "missing", "DIRECT", "GLOBAL", "REJECT"} {
		conn, err := proxydialer.NewByName(name, t).DialContext(context.Background(), "tcp", "127.0.0.1:1")
		if conn != nil {
			conn.Close()
		}
		failed = failed && err != nil
	}
	return map[string]any{"unknownAndBuiltinRejected": failed, "fixedCount": len(r.bindings)}
}
func (r *fullRuntime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	cs := make([]*ownedConn, 0, len(r.connections))
	for c := range r.connections {
		cs = append(cs, c)
	}
	r.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
	if r.admission != nil {
		r.admission.retire()
	}
	for _, p := range r.coreProxies {
		p.Close()
	}
	for _, p := range tunnel.Providers() {
		if closer, ok := p.(io.Closer); ok {
			closer.Close()
		}
	}
	executor.Shutdown()
	if r.admission != nil {
		r.admission.wait(7 * time.Second)
	}
	// A diagnostic bound, not a claim that arbitrary core code can be stopped.
	done := make(chan struct{})
	go func() { r.work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
	}
}
func (r *fullRuntime) snapshot() map[string]any {
	r.mu.Lock()
	count := len(r.connections)
	closed := r.closed
	r.mu.Unlock()
	tracked := 0
	statistic.DefaultManager.Range(func(statistic.Tracker) bool { tracked++; return true })
	out := map[string]any{"standardResolverUnchanged": net.DefaultResolver == r.defaultResolver, "standardTransportUnchanged": http.DefaultTransport == r.defaultTransport, "pending": r.pending.Load(), "peakPending": r.peak.Load(), "openPipes": count, "retired": closed, "goroutines": runtime.NumGoroutine(), "coreTracked": tracked, "systemHosts": resolver.UseSystemHosts, "coreDNSInstalled": resolver.DefaultResolver != nil, "innerGlobalInstalled": inner.GetTunnel() != nil, "slotLimit": cap(r.slots)}
	if r.admission != nil {
		out["admission"] = r.admission.snapshot()
	}
	return out
}
