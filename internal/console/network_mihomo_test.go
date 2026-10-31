package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestMihomoMissingNodesRespectsMode(t *testing.T) {
	c := testConsole(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "direct") }))
	defer server.Close()
	for _, mode := range []string{"proxy", "direct"} {
		s := c.settings
		s.UpstreamMode, s.ProxyBackend, s.nodeSecretError = mode, "mihomo", unavailableProxyNodes
		_, client, err := c.networkFor(s)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Transport.(interface{ Retire() }).Retire()
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

func TestMihomoTemporaryIdentityWarning(t *testing.T) {
	c := testConsole(t)
	path := c.files().ChannelKey
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	c.terms = termsReceipt{} // Warning is available without starting a native core.
	s := runtimeNodeSettings(t, c, 1)
	_, client, err := c.networkFor(s)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Transport.(interface{ Retire() }).Retire()
	c.client.Transport.(interface{ Retire() }).Retire()
	c.client = client
	status := c.proxyNodesStatusLocked()
	if !strings.Contains(status.ActiveError, "节点选择不会保存") {
		t.Fatal("missing temporary identity warning", status)
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
			clients := c.operationClients(context.Background(), upstreamstate.Catalog, upstreamstate.Constraints{Entry: "http://business.invalid/"})
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
			if clients[0] != c.client || testDNS(c).queries.Load() != 0 {
				t.Fatal("proxy did not use the active client or queried direct DNS")
			}
		})
	}
}
