package mihomo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "SWD_CORE_TEST="+t.Name())
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated core: %v\n%s", err, b)
	} else {
		t.Logf("%s", b)
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
	cs := pool.Current("http://business.invalid/")
	var work sync.WaitGroup
	for i := range 40 {
		work.Add(1)
		go func() {
			defer work.Done()
			if err := httpThrough(cs[i%len(cs)].Transport, "/", 3*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	work.Wait()
	for _, c := range cs {
		if err := httpThrough(c.Transport, "/stall", 30*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("warm cancel", err)
		}
		if err := httpThrough(c.Transport, "/", time.Second); err != nil {
			t.Fatal("recovery", err)
		}
	}
	if _, err := Normalize(map[string]any{"type": "ss", "server": "127.0.0.1", "port": 1234, "cipher": "aes-128-gcm", "password": "fixture"}); err != nil {
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

func TestRuntimeSoak(t *testing.T) {
	if os.Getenv("SWD_B2_SOAK") != "1" {
		t.Skip("opt-in 60 second production runtime workload")
	}
	if !runtimeProcess(t) {
		return
	}
	var configs []Config
	for i, p := range fixtureNodes {
		p = maps.Clone(p)
		p["server"] = "127.0.0.1"
		configs = append(configs, Config{ID: fmt.Sprintf("stable-v1-%064x", i+1), Parameters: p})
	}
	r, err := NewRuntime(configs, RuntimeOptions{Home: t.TempDir(), DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pool, err := upstreamrequest.NewMultiPool("socks5", nil, r.Proxies())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	until := time.Now().Add(60 * time.Second)
	var work sync.WaitGroup
	for _, c := range pool.Current("http://business.invalid/") {
		for range 2 {
			work.Add(1)
			go func() {
				defer work.Done()
				requests := 0
				for time.Now().Before(until) {
					// Bound TCP churn below Windows' ephemeral-port range. This
					// checks sustained reuse/cancellation, not host port exhaustion.
					time.Sleep(20 * time.Millisecond)
					if err := httpThrough(c.Transport, "/", 2*time.Second); err != nil {
						t.Error("soak request", c.ID, "after", requests, err)
						return
					}
					requests++
					if requests%10 == 0 {
						if err := httpThrough(c.Transport, "/stall", 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
							t.Error("soak cancel", err)
							return
						}
					}
				}
				t.Logf("worker completed %d successful requests", requests)
			}()
		}
	}
	work.Wait()
	pool.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.admissions {
		a.mu.Lock()
		occupied, workers := a.occupied, a.workers
		a.mu.Unlock()
		if occupied != 0 || workers != 0 {
			t.Fatal("production workload did not drain")
		}
	}
}

func TestRuntimeBadCredentialsNeverReachBusiness(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	p := maps.Clone(fixtureNodes[0])
	p["server"] = "127.0.0.1"
	p["password"] = "wrong-fixture-password"
	r, err := NewRuntime([]Config{{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: p}}, RuntimeOptions{Home: t.TempDir(), DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tr := &http.Transport{DialContext: r.Proxies()[0].Dial}
	defer tr.CloseIdleConnections()
	if err := httpThrough(tr, "/", time.Second); err == nil {
		t.Fatal("wrong credentials bypassed fixed exit")
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
