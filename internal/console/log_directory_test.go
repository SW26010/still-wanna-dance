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
	if err := os.WriteFile(config, []byte(`{"schemaVersion":8,"vrchatLogDir":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"first-user", "second-user"} {
		home := filepath.Join(root, user)
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOME", home)
		c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, "AppData", "LocalLow", "VRChat", "VRChat")
		if c.settings.VRChatLogDir != "" || c.settings.effectiveVRChatLogDir() != want {
			t.Fatalf("automatic directory: %+v", c.settings)
		}
		if err := c.save(c.settings); err != nil {
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
		if saved.VRChatLogDir != "" || saved.DatabasePath != c.files().DefaultDatabase || saved.VideoDir != c.files().DefaultVideo {
			t.Fatalf("persisted environment-dependent defaults: %+v", saved)
		}
		if err := c.writeSnapshot("test", libraryPaths(c.settings), true); err != nil {
			t.Fatal(err)
		}
		data, err = os.ReadFile(c.files().State("test"))
		if err != nil {
			t.Fatal(err)
		}
		var snapshot savedSnapshot[bool]
		if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.DatabasePath != saved.DatabasePath || snapshot.VideoDir != saved.VideoDir {
			t.Fatalf("snapshot retained automatic path: %s, %v", data, err)
		}
		c.Close()
	}
}

func TestManualLogDirectorySwitch(t *testing.T) {
	c := testConsole(t)
	active := c.settings
	s := c.settings
	s.VRChatLogDir = "custom-logs"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	if c.settings != active {
		t.Fatal("saving a log directory changed the running reader")
	}
	c = restartTestConsole(t, c)
	want := filepath.Join(filepath.Dir(c.configPath), "custom-logs")
	if c.settings.VRChatLogDir != want || c.settings.effectiveVRChatLogDir() != want {
		t.Fatalf("manual directory lost: %+v", c.settings)
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || string(fields["vrchatLogDir"]) != `"custom-logs"` {
		t.Fatal("portable relative log directory lost", fields, err)
	}
	s = c.savedSettings
	s.VRChatLogDir = " " // whitespace normalizes to automatic, like an empty path
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	if c.savedSettings.VRChatLogDir != "" || c.settings.effectiveVRChatLogDir() != want {
		t.Fatal("reset did not preserve the active directory until restart")
	}
	c = restartTestConsole(t, c)
	if c.settings.VRChatLogDir != "" || c.settings.effectiveVRChatLogDir() != defaultLogDir() {
		t.Fatal("restart did not restore automatic detection")
	}
}
