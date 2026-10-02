package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAutomaticLogDirectoryFollowsEnvironment(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "settings.json")
	// Legacy configurations saved the detected path without recording intent.
	if err := os.WriteFile(config, []byte(`{"logDir":"old-user/logs"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"first-user", "second-user"} {
		home := filepath.Join(root, user)
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOME", home)
		c, err := New(config, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, "AppData", "LocalLow", "VRChat", "VRChat")
		if c.settings.ManualLogDir || c.settings.LogDir != want {
			t.Fatalf("automatic directory: %+v", c.settings)
		}
		s := c.settings
		s.LogDir = "ignored-api-path"
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		var saved Settings
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.LogDir != "" || saved.ManualLogDir || saved.StorageDir != "still-wanna-dance-data" {
			t.Fatalf("persisted environment-dependent defaults: %+v", saved)
		}
		if err := c.writeSnapshot("test", c.settings, true); err != nil {
			t.Fatal(err)
		}
		data, err = os.ReadFile(config + ".test.json")
		if err != nil {
			t.Fatal(err)
		}
		var snapshot savedSnapshot[bool]
		if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Settings.LogDir != "" {
			t.Fatalf("snapshot retained automatic path: %s, %v", data, err)
		}
		c.Close()
	}
}

func TestManualLogDirectorySwitch(t *testing.T) {
	c := testConsole(t)
	s := c.settings
	s.ManualLogDir = true
	s.LogDir = "custom-logs"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.settings.ManualLogDir || reopened.settings.LogDir != filepath.Join(filepath.Dir(c.configPath), "custom-logs") {
		t.Fatalf("manual directory lost: %+v", reopened.settings)
	}
	s.LogDir = " "
	if err := c.save(s); err == nil {
		t.Fatal("accepted empty manual directory")
	}
	s.ManualLogDir = false
	s.LogDir = "stale-manual-path"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	if c.settings.LogDir != defaultLogDir() {
		t.Fatal("did not restore automatic directory")
	}
}
