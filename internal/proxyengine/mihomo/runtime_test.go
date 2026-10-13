package mihomo

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"
	"still-wanna-dance/internal/upstreamrequest"
)

func runtimeProcess(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SWD_CORE_TEST") == t.Name() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "SWD_CORE_TEST="+t.Name())
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated core: %v\n%s", err, b)
	}
	return false
}

func TestRuntimeFixedProtocolsAndGlobalBoundary(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	var configs []Config
	for i, p := range fixtureNodes {
		p = maps.Clone(p)
		p["server"] = "127.0.0.1"
		configs = append(configs, Config{ID: fmt.Sprintf("stable-v1-%064x", i+1), Parameters: p})
	}
	beforeResolver, beforeTransport := net.DefaultResolver, http.DefaultTransport
	r, err := NewRuntime(configs, RuntimeOptions{Home: t.TempDir(), DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if net.DefaultResolver != beforeResolver || http.DefaultTransport != beforeTransport || resolver.UseSystemHosts {
		t.Fatal("changed direct/system DNS boundary")
	}
	g := executor.GetGeneral()
	if g.Port != 0 || g.SocksPort != 0 || g.MixedPort != 0 || g.RedirPort != 0 || g.TProxyPort != 0 || g.Tun.Enable {
		t.Fatal("inbound or TUN enabled")
	}
	pool, err := upstreamrequest.NewMultiPool("socks5", nil, r.Proxies())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Rebinding the global name map must not move an already captured exit.
	rebound := maps.Clone(tunnel.Proxies())
	for i := range configs {
		rebound[fmt.Sprintf("swd-%d", i)] = rebound["REJECT"]
	}
	tunnel.UpdateProxies(rebound, tunnel.Providers())
	for _, c := range pool.Current("http://business.invalid/") {
		for range 3 {
			if err := httpThrough(c.Transport, "/", time.Second); err != nil {
				t.Fatal("fixed native exit", c.ID, err)
			}
		}
	}
	if _, err := NormalizeNative(map[string]any{"type": "ss", "server": "127.0.0.1", "port": 1234, "cipher": "aes-128-gcm", "password": "fixture"}); err != nil {
		t.Fatal("preview with active core", err)
	}
	pool.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Proxies() {
		if _, err := p.Dial(context.Background(), "tcp", "business.invalid:80"); err != upstreamrequest.ErrRetired {
			t.Fatal(err)
		}
	}
	if _, err := NewRuntime(configs, RuntimeOptions{Home: t.TempDir()}); err == nil {
		t.Fatal("restarted global runtime")
	}
}

func TestRuntimeFailedStartCannotReinitialize(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	configs := []Config{{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: map[string]any{"type": "invalid", "server": "127.0.0.1", "port": 1234}}}
	if _, err := NewRuntime(configs, RuntimeOptions{Home: t.TempDir()}); err == nil {
		t.Fatal("invalid core started")
	}
	configs[0].Parameters = map[string]any{"type": "ss", "server": "127.0.0.1", "port": 1234, "cipher": "aes-128-gcm", "password": "fixture"}
	if _, err := NewRuntime(configs, RuntimeOptions{Home: t.TempDir()}); err == nil {
		t.Fatal("failed core silently retried")
	}
}

type slowCloser struct{ entered, release chan struct{} }

func (s slowCloser) Close() error { close(s.entered); <-s.release; return nil }

func TestRuntimeCloseBudgetIncludesNativeClose(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	blocker := slowCloser{make(chan struct{}), make(chan struct{})}
	r := &Runtime{done: make(chan struct{}), closers: []io.Closer{blocker}}
	start := time.Now()
	if err := r.Close(); err == nil || time.Since(start) > runtimeCloseBudget+time.Second {
		t.Fatal("unbounded or falsely drained Close", err)
	}
	waitSignal(t, blocker.entered)
	close(blocker.release)
	if err := r.Close(); err != nil {
		t.Fatal("cleanup did not finish", err)
	}
}
