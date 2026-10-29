package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableSettingsMove(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original")
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external-logs")
	c, err := newConsoleWithWorkForTest(t, filepath.Join(original, "settings.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	if c.settings.DatabasePath != c.defaultDatabasePath() || c.settings.VideoDir != c.defaultVideoDir() || c.defaultDatabasePath() != filepath.Join(original, "settings.json.d", "data", "database", "storage.sqlite") {
		t.Fatal(c.settings)
	}
	next := c.settings
	createTestDatabase(t, filepath.Join(original, "data", "storage.sqlite"))
	setTestLibraryPaths(&next, "data")
	next.VRChatLogDir = external
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved Settings
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if testStorageRoot(saved) != "data" || saved.VRChatLogDir != external {
		t.Fatal(saved)
	}
	c.Close()
	moved := filepath.Join(root, "moved folder")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	reopened, err := newConsoleWithWorkForTest(t, filepath.Join(moved, "settings.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if testStorageRoot(reopened.settings) != filepath.Join(moved, "data") || reopened.settings.VRChatLogDir != external {
		t.Fatal(reopened.settings)
	}
}
