package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestMihomoDashboardAndConsent(t *testing.T) {
	if !consoleMihomoProcess(t) {
		return
	}
	c := testConsole(t)
	c.terms = termsReceipt{}
	s := runtimeNodeSettings(t, c, 8)
	dial, client, err := c.networkFor(s)
	if err != nil {
		t.Fatal(err)
	}
	c.client.Transport.(interface{ Retire() }).Retire()
	c.client, c.upstreamDial, c.settings, c.savedSettings = client, dial, s, s
	c.loadedNodeCount, c.savedNodeCount = 8, 8
	if client.Transport.(*mihomoTransport).core != nil {
		t.Fatal("core started before consent")
	}
	if _, err := dial(t.Context(), "tcp", "business.invalid:80"); err == nil {
		t.Fatal("pre-consent endpoint usable")
	}
	request := func(method, path, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(`{}`))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/proxy/", "", ""); w.Code != 428 {
		t.Fatal("pre-consent dashboard", w.Code)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	cs := c.client.Transport.(upstreamrequest.Candidates).Current("http://business.invalid/")
	if len(cs) != 1 || cs[0].Mode != "socks5" {
		t.Fatal("not one logical endpoint", len(cs))
	}
	w := request("GET", "/api/mihomo/proxies/SWD", c.token, "")
	var group struct {
		All []string `json:"all"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &group) != nil || len(group.All) != 9 {
		t.Fatal("node subset still applied", w.Code, w.Body.String())
	}
	for _, tc := range [][3]string{{"GET", "/api/mihomo/proxies", ""}, {"PATCH", "/api/mihomo/configs", c.token}, {"POST", "/api/mihomo/upgrade/ui", c.token}} {
		if w := request(tc[0], tc[1], tc[2], ""); w.Code != 403 {
			t.Fatal(tc, w.Code)
		}
	}
	if w := request("GET", "/api/mihomo/version", c.token, "https://example.invalid"); w.Code != 403 {
		t.Fatal("cross origin control")
	}
	for _, p := range []string{"/proxy/", "/proxy/ui/", "/proxy/ui/THIRD_PARTY_NOTICES.md"} {
		w := request("GET", p, "", "")
		if w.Code != 200 {
			t.Fatal(p, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
			t.Fatal("dashboard must allow only same-origin embedding", p, w.Header())
		}
	}
	for _, p := range []string{"/", "/api/mihomo/version"} {
		w := request("GET", p, c.token, "")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("other Console responses must still forbid embedding", p, w.Code, w.Header())
		}
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	// Concurrent native connections and multiple memory subscribers previously
	// raced through Mihomo's unsynchronized memory cache.
	streamContext, stopStreams := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopStreams()
	var streams []*websocket.Conn
	for _, path := range []string{"memory", "memory", "connections"} {
		stream, _, err := websocket.Dial(streamContext, "ws://"+c.address+"/api/mihomo/"+path+"?token="+c.token, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ui.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
		defer stream.CloseNow()
	}
	for i, stream := range streams {
		_, data, err := stream.Read(streamContext)
		var payload struct {
			Inuse uint64 `json:"inuse"`
		}
		if err != nil || json.Unmarshal(data, &payload) != nil || (i < 2 && payload.Inuse == 0) {
			t.Fatal("memory/connections stream", string(data), err)
		}
	}
	stopStreams()
	if evidence := os.Getenv("SWD_DASHBOARD_BROWSER_EVIDENCE"); evidence != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "dashboard-live.mjs"), ui.URL, evidence)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, _, err := websocket.Dial(ctx, "ws://"+c.address+"/api/mihomo/traffic?token="+c.token, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ui.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.CloseNow()
	if _, _, err := stream.Read(ctx); err != nil {
		t.Fatal("native traffic stream", err)
	}
	c.client.Transport.(*mihomoTransport).Retire()
	if _, _, err := stream.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatal("retirement did not close dashboard stream", err)
	}
}
