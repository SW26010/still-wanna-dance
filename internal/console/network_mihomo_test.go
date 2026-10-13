package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Complete core globals get a fresh process, never a production reset hook.
func consoleMihomoProcess(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SWD_CONSOLE_CORE_TEST") == t.Name() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), "SWD_CONSOLE_CORE_TEST="+t.Name())
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("console core process: %v\n%s", err, b)
	}
	return false
}

func runtimeNodeSettings(t *testing.T, c *Console, count int) Settings {
	t.Helper()
	doc := proxyNodeDocument{Version: proxyNodeDocumentVersion}
	for i := range count {
		doc.Nodes = append(doc.Nodes, map[string]any{"type": "ss", "server": "127.0.0.1", "port": float64(10000 + i), "cipher": "aes-128-gcm", "password": "fixture-only"})
	}
	b, _ := json.Marshal(doc)
	s := c.settings
	s.UpstreamMode, s.ProxyBackend, s.nodeSnapshot = "proxy", "mihomo", string(b)
	return s
}

func TestMihomoActiveLimitMembershipIdentityAndRetirement(t *testing.T) {
	if !consoleMihomoProcess(t) {
		return
	}
	c := testConsole(t)
	s := runtimeNodeSettings(t, c, 8)
	dial, client, err := c.mihomoNetworkFor(s, 5)
	if err != nil {
		t.Fatal(err)
	}
	tr := client.Transport.(*mihomoTransport)
	defer tr.Retire()
	known, ready := tr.KnownProxyIDs()
	if len(known) != 8 || !ready || len(tr.proxies) != 5 || len(tr.Current("https://resource.invalid")) != 5 {
		t.Fatal("K capped saved membership or exposed standby")
	}
	directSettings := s
	directSettings.UpstreamMode = "direct"
	_, smaller, err := c.mihomoNetworkFor(directSettings, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer smaller.Transport.(*mihomoTransport).Retire()
	other, _ := smaller.Transport.(*mihomoTransport).KnownProxyIDs()
	if !slices.Equal(known, other) {
		t.Fatal("K changed identities")
	}
	if got := c.retainedProxyNodes(s, "external"); got == nil {
		t.Fatal("backend switch lost imported membership")
	}
	c.client.CloseIdleConnections()
	c.client.Transport.(interface{ Retire() }).Retire()
	c.client, c.settings = client, s
	selections := c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: "http://resource.invalid"})
	if len(selections) != 5 || selections[0].Channel.ID != tr.Current("http://resource.invalid")[0].ID {
		t.Fatal("cold selection ignored active pool")
	}
	for _, selection := range selections {
		if selection.Channel.Mode != "socks5" || selection.Result.State != "" {
			t.Fatal("cold proxy claimed direct or measured state")
		}
	}
	tr.Retire()
	if len(tr.Current("http://resource.invalid")) != 0 {
		t.Fatal("retired candidates visible")
	}
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, net.ErrClosed) {
		t.Fatal("raw dial survived retirement", err)
	}
	for _, node := range tr.proxies {
		if _, err := node.Dial(context.Background(), "tcp", "resource.invalid:80"); !errors.Is(err, upstreamrequest.ErrRetired) {
			t.Fatal("node survived retirement")
		}
	}
}

func TestMihomoMissingNodesForcedFailureAndAutoDirect(t *testing.T) {
	c := testConsole(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "direct") }))
	defer server.Close()
	for _, mode := range []string{"proxy", "auto"} {
		s := c.settings
		s.UpstreamMode, s.ProxyBackend, s.nodeSecretError = mode, "mihomo", unavailableProxyNodes
		_, client, err := c.networkFor(s)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Transport.(*mihomoTransport).Retire()
		if _, ready := client.Transport.(*mihomoTransport).KnownProxyIDs(); ready {
			t.Fatal("unreadable store authoritative")
		}
		response, err := client.Get(server.URL)
		if mode == "proxy" {
			if err == nil {
				response.Body.Close()
				t.Fatal("forced proxy silently dialed direct")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		}
	}
}

func TestMihomoColdAutoUsesDirectWithoutMonitorOrDNSCache(t *testing.T) {
	if !consoleMihomoProcess(t) {
		return
	}
	c := testConsole(t)
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }))
	defer server.Close()
	s := runtimeNodeSettings(t, c, 1)
	s.UpstreamMode = "auto"
	_, client, err := c.networkFor(s)
	if err != nil {
		t.Fatal(err)
	}
	c.client.Transport.(interface{ Retire() }).Retire()
	c.client, c.settings, c.apiBase = client, s, server.URL
	clients := c.operationClients(upstreamstate.PlaybackURL, upstreamstate.Constraints{Entry: server.URL})
	if len(clients) != 2 {
		t.Fatalf("missing cold direct path: %d clients", len(clients))
	}
	if len(c.dns.CurrentAddresses("127.0.0.1")) != 0 {
		t.Fatal("selection unexpectedly populated DNS")
	}
	if _, pinned := clients[0].Transport.(interface{ ChannelID() string }); pinned {
		t.Fatal("unresolved direct path claims a pinned channel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := c.resolveNode(ctx, 42, "cf"); err != nil || got != target {
		t.Fatalf("cold auto failed: %s %v", got, err)
	}
	client.Transport.(*mihomoTransport).Retire()
	if _, err := clients[0].Get(server.URL); !errors.Is(err, net.ErrClosed) {
		t.Fatal("retired direct fallback allowed a request", err)
	}
}

func TestMihomoBusinessFallbackReachesFifthProxy(t *testing.T) {
	const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
	var directCalls atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }))
	defer proxy.Close()
	for _, mode := range []string{"proxy", "cold-auto", "warm-auto"} {
		t.Run(mode, func(t *testing.T) {
			if !consoleMihomoProcess(t) {
				return
			}
			c := testConsole(t)
			s := runtimeNodeSettings(t, c, 5)
			if mode != "proxy" {
				s.UpstreamMode = "auto"
			}
			_, client, err := c.networkFor(s)
			if err != nil {
				t.Fatal(err)
			}
			tr := client.Transport.(*mihomoTransport)
			// Controlled fixed dialers isolate the business selection/HTTP retry
			// contract; protocol interoperability has its own loopback fixtures.
			calls := make([]int, 5)
			var exits []upstreamrequest.Proxy
			for i, id := range tr.known {
				exits = append(exits, upstreamrequest.Proxy{ID: id, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					calls[i]++
					if i < 4 {
						return nil, errors.New("fixture offline")
					}
					return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(proxy.URL, "http://"))
				}})
			}
			tr.Pool.Close()
			tr.Pool, err = upstreamrequest.NewMultiPool(channelMode(s.UpstreamMode), c.dns, exits)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "warm-auto" {
				c.dns.cache = map[string]dnsEntry{"127.0.0.1": {ips: []string{"127.0.0.1", "127.0.0.2", "127.0.0.3", "127.0.0.4", "127.0.0.5", "127.0.0.6"}, until: time.Now().Add(time.Hour)}}
			}
			c.client.Transport.(interface{ Retire() }).Retire()
			c.client, c.settings, c.apiBase = client, s, direct.URL
			selections := c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: direct.URL})
			want := 6
			if mode == "proxy" {
				want = 5
			}
			if len(selections) != want {
				t.Fatalf("candidate budget: got %d want %d", len(selections), want)
			}
			before := directCalls.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if got, err := c.resolveNode(ctx, 42, "cf"); err != nil || got != target {
				t.Fatalf("fifth proxy omitted: calls=%v error=%v", calls, err)
			}
			if !slices.Equal(calls, []int{1, 1, 1, 1, 1}) {
				t.Fatalf("wrong fixed attempts: %v", calls)
			}
			if mode == "proxy" && directCalls.Load() != before || mode != "proxy" && directCalls.Load() != before+1 {
				t.Fatal("wrong direct eligibility")
			}
		})
	}
}

func TestMihomoCorruptKeyPreservedWithFreshIdentity(t *testing.T) {
	c := testConsole(t)
	if err := os.WriteFile(c.files().ChannelKey, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s := runtimeNodeSettings(t, c, 1)
	s.UpstreamMode = "direct" // Identity/membership do not require another core.
	var last string
	for range 2 {
		_, client, err := c.networkFor(s)
		if err != nil {
			t.Fatal(err)
		}
		tr := client.Transport.(*mihomoTransport)
		ids, ready := tr.KnownProxyIDs()
		if ready || tr.identityWarning == "" || len(ids) != 1 || ids[0] == last {
			t.Fatal("temporary identity reused or missing warning")
		}
		last = ids[0]
		tr.Retire()
	}
	b, _ := os.ReadFile(c.files().ChannelKey)
	if string(b) != "broken" {
		t.Fatal("bad key overwritten")
	}
}

func TestConsoleMihomoProtocolsColdHTTP(t *testing.T) {
	// Reuse the adapter's independent loopback server; never link inbound
	// packages into the console test process or access public proxies.
	dir := t.TempDir()
	exe := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-tags=no_easytier", "-o", exe, "../proxyengine/mihomo/testfixture")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %s %v", b, err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	server := exec.CommandContext(ctx, exe, manifest)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	var fixture struct {
		Nodes []map[string]any `json:"nodes"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(manifest); err == nil && json.Unmarshal(b, &fixture) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture readiness timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, node := range fixture.Nodes {
		t.Run(fmt.Sprint(node["type"]), func(t *testing.T) {
			if !consoleMihomoProcess(t) {
				return
			}
			c := testConsole(t)
			// Keep real controlled DNS resolution bypassed only for the local
			// proxy fixture; the destination remains a remote-resolved hostname.
			node["server"] = "127.0.0.1"
			doc, _ := json.Marshal(proxyNodeDocument{Version: proxyNodeDocumentVersion, Nodes: []map[string]any{node}})
			s := c.settings
			s.ProxyBackend, s.UpstreamMode, s.nodeSnapshot = "mihomo", "proxy", string(doc)
			_, client, err := c.networkFor(s)
			if err != nil {
				t.Fatal(err)
			}
			c.client.Transport.(interface{ Retire() }).Retire()
			c.client, c.settings = client, s
			clients := c.operationClients(upstreamstate.Catalog, upstreamstate.Constraints{Entry: "http://business.invalid/"})
			if len(clients) != 1 {
				t.Fatal("wrong cold clients")
			}
			resp, err := clients[0].Get("http://business.invalid/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != "mihomo-embedded-business-response\n" {
				t.Fatal("wrong proxy response", err)
			}
			if tr, ok := clients[0].Transport.(interface{ ChannelID() string }); !ok || !strings.Contains(tr.ChannelID(), "/stable-v1-") {
				t.Fatal("cold channel identity unavailable")
			}
		})
	}
}
