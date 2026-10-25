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

// Real Console routes, config persistence and fresh browser contexts; no game,
// production settings, hosts changes or gateway startup.
func TestThemeBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_THEME_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_THEME_BROWSER_EVIDENCE for real Console theme integration")
	}
	c := testConsole(t)
	defer func() { c.Close() }()
	for _, phase := range []string{"change", "reopen"} {
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "theme-live.mjs"), ui.URL, phase, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		if c.settings.Theme != "dark" || c.DesktopState().Theme != "dark" || c.settings != c.savedSettings || c.service != nil {
			t.Fatal("browser theme not applied independently to console and desktop")
		}
		_, fields, err := readConfigFile(c.configPath)
		if err != nil || string(fields["theme"]) != `"dark"` {
			t.Fatal("browser selection missing from config", err)
		}
		if phase == "change" {
			c = restartTestConsole(t, c)
		}
	}
}
