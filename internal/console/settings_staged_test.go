package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the production load/close boundary instead of mutating a live runtime.
func restartTestConsole(t *testing.T, c *Console) *Console {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	next.videoAddress, next.httpsAddress = c.videoAddress, c.httpsAddress
	next.checksumURL, next.apiBase = c.checksumURL, c.apiBase
	t.Cleanup(func() { next.Close() })
	return next
}

func TestSavedSettingsDoNotChangeRuntime(t *testing.T) {
	c := testConsole(t)
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	active, engine, client := c.settings, c.service, c.client
	next := active
	next.StorageDir = filepath.Join(t.TempDir(), "new-cache")
	next.SOCKS5Address, next.UpstreamMode = "127.0.0.1:1080", "socks5"
	next.SOCKS5Username, next.SOCKS5Password = "user", "secret"
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	if c.settings != active || c.service != engine || c.client != client {
		t.Fatal("save changed runtime")
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status struct {
		Settings, ActiveSettings Settings
		RestartRequired          bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.RestartRequired || status.Settings.StorageDir != next.StorageDir || status.ActiveSettings.StorageDir != active.StorageDir || status.Settings.SOCKS5Password != "" || status.ActiveSettings.SOCKS5Password != "" {
		t.Fatalf("incorrect snapshots: %s", w.Body.String())
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.service != engine || c.settings != active {
		t.Fatal("CDN restart applied saved settings")
	}
	c = restartTestConsole(t, c)
	if c.settings != next || c.savedSettings != next {
		t.Fatal("application restart did not apply settings")
	}
}

func TestSettingsSaveFailureAndRevert(t *testing.T) {
	c := testConsole(t)
	active := c.settings
	next := active
	next.QueuePrefetchCount++
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	if err := c.save(active); err != nil {
		t.Fatal(err)
	}
	if c.savedSettings != c.settings {
		t.Fatal("revert still requires restart")
	}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	next.MaxCacheBytes = -1
	if err := c.save(next); err == nil {
		t.Fatal("invalid settings saved")
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || string(before) != string(after) || c.savedSettings != active {
		t.Fatal("failed save changed settings")
	}
	// A destination directory forces the final replacement to fail.
	c.configPath = t.TempDir()
	next.MaxCacheBytes = 1
	if err := c.save(next); err == nil || c.savedSettings != active {
		t.Fatal("failed write changed snapshot")
	}
}
