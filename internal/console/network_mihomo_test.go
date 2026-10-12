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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamstate"
)

func runtimeNodeSettings(t *testing.T, c *Console, count int) Settings {
	t.Helper()
	doc := proxyNodeDocument{Version: 1}
	for i := range count {
		doc.Nodes = append(doc.Nodes, map[string]any{"type": "ss", "server": "127.0.0.1", "port": float64(10000 + i), "cipher": "aes-128-gcm", "password": "fixture-only"})
	}
	b, _ := json.Marshal(doc)
	s := c.settings
	s.UpstreamMode, s.ProxyBackend, s.nodeSnapshot = "proxy", "mihomo", string(b)
	return s
}

func TestMihomoActiveLimitMembershipIdentityAndRetirement(t *testing.T) {
	c := testConsole(t)
	s := runtimeNodeSettings(t, c, 8)
	dial, client, err := c.mihomoNetworkFor(s, 5)
	if err != nil {
		t.Fatal(err)
	}
	tr := client.Transport.(*mihomoTransport)
	defer tr.Retire()
	known, ready := tr.KnownProxyIDs()
	if len(known) != 8 || !ready || len(tr.nodes) != 5 || len(tr.Current("https://resource.invalid")) != 5 {
		t.Fatal("K capped saved membership or exposed standby")
	}
	_, smaller, err := c.mihomoNetworkFor(s, 2)
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
	for _, node := range tr.nodes {
		if _, err := node.DialContext(context.Background(), "tcp", "resource.invalid:80"); !errors.Is(err, net.ErrClosed) {
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

func TestMihomoCorruptKeyPreservedWithFreshIdentity(t *testing.T) {
	c := testConsole(t)
	if err := os.WriteFile(c.files().ChannelKey, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	s := runtimeNodeSettings(t, c, 1)
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
			c := testConsole(t)
			// Keep real controlled DNS resolution bypassed only for the local
			// proxy fixture; the destination remains a remote-resolved hostname.
			c.dns.cache = map[string]dnsEntry{"proxy.invalid": {ips: []string{"127.0.0.1"}, until: time.Now().Add(time.Hour)}}
			doc, _ := json.Marshal(proxyNodeDocument{Version: 1, Nodes: []map[string]any{node}})
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
