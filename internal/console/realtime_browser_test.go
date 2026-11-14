package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Real Console routes and assets; isolated storage, no gateway or game.
func TestRealtimeNetworkBrowsers(t *testing.T) {
	evidence := os.Getenv("SWD_REALTIME_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_REALTIME_BROWSER_EVIDENCE for multi-page browser integration")
	}
	c := testConsole(t)
	stubNetworkDiagnostics(t, c)
	var probes, networkStreams, legacyStreams atomic.Int32
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__realtime/probe" {
			writeJSON(w, probes.Add(1))
			return
		}
		if r.URL.Path == "/__realtime/streams" {
			writeJSON(w, map[string]int32{"network": networkStreams.Load(), "legacy": legacyStreams.Load()})
			return
		}
		switch r.URL.Path {
		case "/api/network/events":
			networkStreams.Add(1)
			defer networkStreams.Add(-1)
		case "/api/network/connectivity/events", "/api/network/calibration/events":
			legacyStreams.Add(1)
			defer legacyStreams.Add(-1)
		}
		c.ServeHTTP(w, r)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "realtime-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("multi-page browser: %v\n%s", err, output)
	}
}
