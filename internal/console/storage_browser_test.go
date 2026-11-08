package console

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
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
	cacheDir := t.TempDir()
	run := func(phase string) {
		t.Helper()
		c.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
			if dialog.Title == "选择临时视频缓存目录" {
				return cacheDir, nil
			}
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
		cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "storage-live.mjs"), ui.URL, database, video, evidence, phase, logDir, cacheDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
	}
	run("configure")
	if c.store == nil || c.service != nil || c.settings.DatabasePath != c.defaultDatabasePath() || c.savedSettings.DatabasePath != database || c.settings.BlockCacheDir != "" || c.savedSettings.BlockCacheDir != cacheDir {
		t.Fatal("selection activated before restart", c.storageOpenError)
	}
	if c.settings.VRChatLogDir != "" || c.savedSettings.VRChatLogDir != logDir || c.settings.DownloadConcurrencySoftLimit != 16 || c.savedSettings.DownloadConcurrencySoftLimit != 32 {
		t.Fatal("browser save lost settings or applied them before restart")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := desktop.NewIdentity(config)
	if err != nil {
		t.Fatal(err)
	}
	work, err := cacheproxy.OpenWorkDirectory(apppaths.WorkForCacheRoot(identity.Executable, identity.Config, cacheDir), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { work.Close() })
	c, err = newConsole(config, "127.0.0.1:0", newFixtureDNS(), work)
	if err != nil {
		t.Fatal(err)
	}
	if c.store == nil || c.settings.VideoDir != video || c.settings.BlockCacheDir != cacheDir || c.work.Path() != work.Path() || c.service != nil {
		t.Fatal("restart did not apply independent paths", c.storageOpenError)
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

func TestStorageChoicesBrowser(t *testing.T) {
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
	portable, root := t.TempDir(), t.TempDir()
	c.identity.Executable = filepath.Join(portable, "SWD.exe")
	cache := filepath.Join(root, "cache")
	videos := filepath.Join(root, "videos")
	badVideo := filepath.Join(cache, "nested-videos")
	badDatabase := filepath.Join(cache, "new", "library.sqlite")
	database := filepath.Join(root, "database", "library.sqlite")
	for _, dir := range []string{filepath.Join(portable, "custom"), filepath.Join(portable, "work"), badVideo, videos} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	c.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
		counts[dialog.Title]++
		switch dialog.Title {
		case "选择临时视频缓存目录":
			switch counts[dialog.Title] {
			case 1:
				return filepath.Join(portable, "custom"), nil
			case 3:
				return root, nil
			case 4:
				return filepath.Dir(config), nil
			case 6:
				return filepath.Join(portable, "work"), nil
			}
			return cache, nil
		case "选择完整视频目录":
			if counts[dialog.Title] == 1 {
				return badVideo, nil
			}
			return videos, nil
		case "选择或新建 SWD 数据库":
			if counts[dialog.Title] == 1 {
				return badDatabase, nil
			}
			return database, nil
		default:
			return "", nil
		}
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "storage-choices-live.mjs"), ui.URL, database, videos, cache, filepath.Join(evidence, "choices"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else if len(output) != 0 {
		t.Log(string(output))
	}
	if _, err := os.Stat(badDatabase); err != nil {
		t.Fatal("database was not created after the draft became valid", err)
	}
	if c.savedSettings.DatabasePath != database || c.savedSettings.VideoDir != videos || c.savedSettings.BlockCacheDir != cache {
		t.Fatal("browser did not save the validated choices")
	}
	if c.settings.DatabasePath == database || c.settings.VideoDir == videos || c.settings.BlockCacheDir != "" {
		t.Fatal("browser applied staged choices without restarting")
	}
	c = restartTestConsole(t, c)
	if c.settings.DatabasePath != database || c.settings.VideoDir != videos || c.settings.BlockCacheDir != cache || c.store == nil {
		t.Fatal("validated choices did not survive restart", c.storageOpenError)
	}
}
