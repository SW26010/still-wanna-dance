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

	"still-wanna-dance/internal/buildinfo"
)

// Real Console and browser with a controlled GitHub response and isolated data.
func TestUpdateBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_UPDATE_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_UPDATE_BROWSER_EVIDENCE for real Console update integration")
	}
	c := testConsole(t)
	previousVersion, previousKind, previousRevision := buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision
	buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision = "v1.0.0-beta.4", "rebuild", strings.Repeat("a", 40)
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision = previousVersion, previousKind, previousRevision
	})
	oldClient := c.client
	t.Cleanup(func() { c.client = oldClient })
	var calls atomic.Int32
	c.client = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return releaseResponse(http.StatusOK, `[{"tag_name":"v1.0.0"}]`), nil
	})}
	c.StartUpdateCheck()
	c.mu.Lock()
	done := c.updateDone
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "updates-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one startup and one manual GitHub request, got %d", calls.Load())
	}
}
