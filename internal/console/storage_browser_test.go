package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestStorageLocationsBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_STORAGE_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_STORAGE_BROWSER_EVIDENCE for storage browser integration")
	}
	config := filepath.Join(t.TempDir(), "config.json")
	c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(t.TempDir(), "完整 视频")
	database := filepath.Join(t.TempDir(), "资料", "library.sqlite")
	logDir := filepath.Join(t.TempDir(), "游戏日志")
	run := func(phase string) {
		t.Helper()
		c.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
			if dialog.Title == "选择 VRChat 日志目录" {
				return logDir, nil
			}
			if dialog.Mode == "folder" {
				return video, nil
			}
			return database, nil
		}
		ui := httptest.NewUnstartedServer(c)
		c.address = ui.Listener.Addr().String()
		ui.Start()
		defer ui.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "storage-live.mjs"), ui.URL, database, video, evidence, phase, logDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
	}
	run("configure")
	if c.store == nil || c.service != nil || c.settings.DatabasePath != c.defaultDatabasePath() || c.savedSettings.DatabasePath != database {
		t.Fatal("selection activated before restart", c.storeOpenError)
	}
	if c.settings.VRChatLogDir != "" || c.savedSettings.VRChatLogDir != logDir || c.settings.DownloadConcurrencySoftLimit != 16 || c.savedSettings.DownloadConcurrencySoftLimit != 32 {
		t.Fatal("browser save lost settings or applied them before restart")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if c.store == nil || c.settings.VideoDir != video || c.service != nil {
		t.Fatal("restart did not apply independent paths", c.storeOpenError)
	}
	run("reopened")
	if c.settings.VRChatLogDir != logDir || c.savedSettings.VRChatLogDir != "" || c.settings.DownloadConcurrencySoftLimit != 32 {
		t.Fatal("browser reset changed the active directory or lost the concurrency setting")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if c.settings.VRChatLogDir != "" || c.settings.effectiveVRChatLogDir() != defaultLogDir() || c.savedSettings != c.settings || c.service != nil {
		t.Fatal("automatic directory did not survive restart")
	}
	_, fields, err := readConfigFile(config)
	if err != nil || string(fields["vrchatLogDir"]) != `""` || fields["manualLogDir"] != nil || fields["logDir"] != nil {
		t.Fatal("browser persisted an automatic path or obsolete fields", fields, err)
	}
	run("automatic")
}
