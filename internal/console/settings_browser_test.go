package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/faults"
)

func TestSettingsValidationBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_SETTINGS_VALIDATION_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_SETTINGS_VALIDATION_BROWSER_EVIDENCE for real settings validation integration")
	}
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "settings-validation-live.mjs"), ui.URL, evidence)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if open, cause := center.Take(); open {
		t.Fatalf("validation requested WebUI opening: %v", cause)
	}
	if c.settings.UpstreamMode != "direct" || c.savedSettings.SOCKS5Address != "localhost:7891" {
		t.Fatal("wrong save/activation boundary")
	}
}

func TestSettingsSeparateBrowsers(t *testing.T) {
	evidence := os.Getenv("SWD_SETTINGS_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_SETTINGS_BROWSER_EVIDENCE for real Console concurrent settings integration")
	}
	c := testConsole(t)
	active := c.settings
	next := c.savedSettings
	next.VideoStorageMode = "unlimited"
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	videoDir := t.TempDir()
	video := filepath.Join(videoDir, strings.Repeat("a", 32)+".mp4")
	if err := os.WriteFile(video, []byte("existing video"), 0600); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "settings-live.mjs"), ui.URL, evidence, videoDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if c.settings != active || c.service != nil {
		t.Fatal("saving settings applied runtime settings or started the gateway")
	}
	c = restartTestConsole(t, c)
	if c.settings.LogLevel != "debug" || !c.settings.ExitWithVRChat || c.settings.DownloadConcurrencySoftLimit != 17 || c.settings.VideoDir != videoDir || c.settings.VideoStorageMode != "none" {
		t.Fatal("restart lost a browser's saved settings")
	}
	if data, err := os.ReadFile(video); err != nil || string(data) != "existing video" {
		t.Fatal("settings confirmation changed the existing video", err)
	}
}
