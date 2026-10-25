package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Isolated real Console, DPAPI receipt and browser. Automatic acceleration is
// disabled before the fresh-consent scenario; no production configuration is used.
func TestTermsBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_TERMS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_TERMS_BROWSER_EVIDENCE for real Console consent integration")
	}
	c := testConsole(t)
	settings := c.settings
	settings.AutoAccelerate = false
	if err := c.save(settings); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if err := os.Remove(c.files().Terms); err != nil {
		t.Fatal(err)
	}
	c.terms = termsReceipt{}
	for _, phase := range []string{"accept", "reopen"} {
		c.AutoStart()
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "terms-live.mjs"), ui.URL, phase, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		if !c.termsAccepted() || c.httpServer != nil || c.autoStartPending {
			t.Fatal("consent or manual startup preference lost")
		}
		if phase == "accept" {
			c = restartTestConsole(t, c)
		}
	}
}
