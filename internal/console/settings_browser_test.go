package console

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/faults"
)

func TestMihomoConfigurationBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_MIHOMO_CONFIG_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_MIHOMO_CONFIG_BROWSER_EVIDENCE for real configuration import integration")
	}
	if !consoleMihomoProcess(t) {
		return
	}
	c := testConsole(t)
	content := "# preserved original\nproxies: [{name: local, type: ss, server: localhost, port: 1234, cipher: aes-128-gcm, password: private-fixture}]\nproxy-groups: [{name: original, type: select, proxies: [local]}]\n"
	fixture := filepath.Join(t.TempDir(), "机场配置.yaml")
	if err := os.WriteFile(fixture, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.URL.Path == "/invalid" {
			io.WriteString(w, "not a configuration")
			return
		}
		io.WriteString(w, content)
	}))
	defer source.Close()
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "mihomo-configuration-live.mjs"), ui.URL, source.URL+"/?private-source-token=secret", fixture, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if downloads.Load() != 2 || c.editor.savedSettings.nodeSnapshot != content || c.settings.nodeSnapshot != "" || c.editor.savedSettings.ProxyBackend != "mihomo" || c.editor.savedSettings.nodeInfo.Name != "URL 配置" || c.editor.savedSettings.nodeInfo.ImportedAt.IsZero() {
		t.Fatal("wrong acquisition or activation boundary")
	}
	data, err := os.ReadFile(c.configPath)
	if err != nil || bytes.Contains(data, []byte("private-fixture")) || bytes.Contains(data, []byte("private-source-token")) || bytes.Contains(data, []byte(source.URL)) {
		t.Fatal("source or unprotected configuration persisted")
	}
	loaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.nodeSnapshot != content || loaded.settings.nodeInfo != c.editor.savedSettings.nodeInfo {
		t.Fatal("restart did not preserve original configuration")
	}
}

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
	if c.settings.UpstreamMode != "direct" || c.editor.savedSettings.SOCKS5Address != "localhost:7891" {
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
	next := c.editor.savedSettings
	next.VideoStorageMode = "unlimited"
	if err := c.editor.save(next); err != nil {
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
