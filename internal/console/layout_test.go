package console

import (
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/desktop"
)

func TestPortableLayoutCreatesOnlyUsedFiles(t *testing.T) {
	root := t.TempDir()
	path := apppaths.DefaultConfig(filepath.Join(root, "SWD.exe"))
	c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(c.files().Root)
	if err != nil || len(entries) != 3 || entries[0].Name() != "config.json" || entries[1].Name() != "data" || entries[2].Name() != "videos" {
		t.Fatal("unused feature created files", entries, err)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.files().Terms); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"channel.key", "throughput.json", "inventory.json", "batch-last-success.json", "batch-last-attempt.json", "video-import.json", "mihomo"} {
		if _, err := os.Stat(filepath.Join(c.files().StateDir, name)); !os.IsNotExist(err) {
			t.Fatal("unused feature created", name, err)
		}
	}
}

func TestCustomConfigurationsKeepIndependentStateAfterMove(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	external := filepath.Join(root, "external")
	keys := map[string]string{}
	for i, name := range []string{"config.json", "a.json", "b.json"} {
		path := filepath.Join(old, name)
		lease, err := desktop.LockConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:0")
		if err != nil {
			lease.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close(); lease.Close() })
		s := c.settings
		s.DatabasePath, s.VideoDir = c.defaultDatabasePath(), c.defaultVideoDir()
		if i == 2 {
			setTestLibraryPaths(&s, external)
			createTestDatabase(t, s.DatabasePath)
		}
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		if i != 1 {
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
		}
		id, err := testNodeIdentity(c)
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = id
		if err := c.writeSnapshot("test", libraryPaths(c.savedSettings), i+1); err != nil {
			t.Fatal(err)
		}
		if other, err := desktop.LockConfig(path); err == nil {
			other.Close()
			t.Fatal("same config not locked")
		}
		c.Close()
		lease.Close()
	}
	moved := filepath.Join(root, "moved")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"config.json", "a.json", "b.json"} {
		c, err := newConsoleWithWorkForTest(t, filepath.Join(moved, name), "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if c.termsAccepted() != (i != 1) {
			t.Fatal("terms crossed configurations", name)
		}
		id, err := testNodeIdentity(c)
		if err != nil || id != keys[name] {
			t.Fatal("identity lost after move", name, err)
		}
		if got, err := readSnapshot[int](c, "test"); err != nil || got != i+1 {
			t.Fatal("snapshot lost after move", got, err)
		}
		if i == 2 && testStorageRoot(c.settings) != external {
			t.Fatal("external path changed")
		}
		c.Close()
	}
	if keys["config.json"] == keys["a.json"] || keys["a.json"] == keys["b.json"] {
		t.Fatal("shared channel identity")
	}
}
