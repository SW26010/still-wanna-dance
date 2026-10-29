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

// Real Console and browser, starting with no config, library or consent record.
// Only DNS, hosts and listener ports are isolated from the user's environment.
func TestFirstRunBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_FIRST_RUN_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_FIRST_RUN_BROWSER_EVIDENCE for fresh desktop browser integration")
	}
	c, err := newConsole(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.hosts, _ = fixtureHostsController(t, hostsBaseline)
	c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
	c.settings.VRChatLogDir = t.TempDir()
	c.savedSettings = c.settings
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "first-run-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if !c.termsAccepted() || c.store == nil || c.activation.Phase != "waiting" || c.savedSettings != c.settings {
		t.Fatal("first-run default experience did not become ready", c.activation, c.storeOpenError)
	}
}
