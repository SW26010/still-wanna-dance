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
	c, err := New(filepath.Join(original, "settings.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	if c.settings.SongsDir != filepath.Join(original, "wannadance-song") {
		t.Fatal(c.settings)
	}
	if err := c.save(Settings{SongsDir: "songs", CacheDir: "cache", LogDir: external}); err != nil {
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
	if saved.SongsDir != "songs" || saved.CacheDir != "cache" || saved.LogDir != external {
		t.Fatal(saved)
	}
	moved := filepath.Join(root, "moved folder")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(filepath.Join(moved, "settings.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.settings.SongsDir != filepath.Join(moved, "songs") || reopened.settings.CacheDir != filepath.Join(moved, "cache") || reopened.settings.LogDir != external {
		t.Fatal(reopened.settings)
	}
}
