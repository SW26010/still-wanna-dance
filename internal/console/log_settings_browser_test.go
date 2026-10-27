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

// Optional real Edge + Console persistence check. The fixture supplies consent
// only inside this test; no gateway, monitor, production settings or hosts run.
func TestLogSettingsBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_LOG_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_LOG_BROWSER_EVIDENCE for real Console logging settings integration")
	}
	c := testConsole(t)
	defer func() { c.Close() }()
	for _, phase := range []string{"off", "debug", "info", "reopen"} {
		active := c.settings.LogLevel
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "log-settings-live.mjs"), ui.URL, phase, active, evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		want := phase
		if phase == "reopen" {
			want = "info"
		}
		if c.settings.LogLevel != active || c.savedSettings.LogLevel != want || c.service != nil {
			t.Fatal("save applied runtime settings or started the gateway")
		}
		level, err := ReadLogLevel(c.configPath)
		if err != nil || level.String() != want {
			t.Fatal("startup level differs from browser choice", level, err)
		}
		c = restartTestConsole(t, c)
		if c.settings.LogLevel != want || c.settings != c.savedSettings {
			t.Fatal("saved choice not applied after Console restart")
		}
	}
}
