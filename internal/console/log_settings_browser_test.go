package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
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
	for _, tc := range []struct {
		phase, level string
		budgetMiB    int64
	}{
		{"budget", "info", 64},
		{"off", "off", 64},
		{"debug", "debug", 20},
		{"info", "info", 20},
		{"reopen", "info", 20},
	} {
		active := c.settings
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "log-settings-live.mjs"),
			ui.URL, tc.phase, active.LogLevel, strconv.FormatInt(active.LogBudgetBytes/applog.MaxBytes, 10), tc.level, strconv.FormatInt(tc.budgetMiB, 10), evidence)
		output, err := cmd.CombinedOutput()
		cancel()
		ui.Close()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", tc.phase, err, output)
		}
		t.Log(string(output))
		wantBytes := tc.budgetMiB * applog.MaxBytes
		if c.settings != active || c.editor.savedSettings.LogLevel != tc.level || c.editor.savedSettings.LogBudgetBytes != wantBytes || c.service != nil {
			t.Fatal("save applied runtime settings or started the gateway")
		}
		level, budget, err := ReadLogSettings(c.configPath)
		if err != nil || level.String() != tc.level || budget != wantBytes {
			t.Fatal("startup logging differs from browser choices", level, budget, err)
		}
		c = restartTestConsole(t, c)
		if c.settings.LogLevel != tc.level || c.settings.LogBudgetBytes != wantBytes || c.settings != c.editor.savedSettings {
			t.Fatal("saved choice not applied after Console restart")
		}
	}
}
