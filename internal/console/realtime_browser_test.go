package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real Console routes and assets; isolated storage, no gateway or game.
func TestRealtimeNetworkBrowsers(t *testing.T) {
	testRealtimeBrowsers(t, "network")
}

func TestRealtimeHistoryAndBlocksBrowsers(t *testing.T) {
	for _, view := range []string{"history", "blocks"} {
		t.Run(view, func(t *testing.T) { testRealtimeBrowsers(t, view) })
	}
}

func testRealtimeBrowsers(t *testing.T, view string) {
	evidence := os.Getenv("SWD_REALTIME_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_REALTIME_BROWSER_EVIDENCE for multi-page browser integration")
	}
	c := testConsole(t)
	stubNetworkDiagnostics(t, c)
	var probes, websocketStreams, sseStreams atomic.Int32
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__realtime/probe" {
			writeJSON(w, probes.Add(1))
			return
		}
		if r.URL.Path == "/__realtime/streams" {
			writeJSON(w, map[string]int32{"websocket": websocketStreams.Load(), "sse": sseStreams.Load()})
			return
		}
		switch r.URL.Path {
		case "/api/network/events", "/api/history/events", "/api/block-demands/events", "/api/network/connectivity/events", "/api/network/calibration/events":
			streams := &sseStreams
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				streams = &websocketStreams
			}
			streams.Add(1)
			defer streams.Add(-1)
		}
		c.ServeHTTP(w, r)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "realtime-live.mjs"), ui.URL, filepath.Join(evidence, view), view)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("multi-page browser: %v\n%s", err, output)
	}
}
