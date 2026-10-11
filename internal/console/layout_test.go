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
	c, err := New(path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	entries, err := os.ReadDir(c.files().Root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatal("unused feature created files", entries, err)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.files().Terms); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state", "storage", "channel.key"} {
		if _, err := os.Stat(filepath.Join(c.files().Root, name)); !os.IsNotExist(err) {
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
		c, err := New(path, "127.0.0.1:0")
		if err != nil {
			lease.Close()
			t.Fatal(err)
		}
		if i == 2 {
			s := c.settings
			s.StorageDir = external
			if err := c.save(s); err != nil {
				t.Fatal(err)
			}
		}
		if i != 1 {
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
		}
		id, err := c.proxyIdentity(Settings{SOCKS5Address: "127.0.0.1:1080"})
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = id
		if err := c.writeSnapshot("test", c.savedSettings.StorageDir, i+1); err != nil {
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
		c, err := New(filepath.Join(moved, name), "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if c.termsAccepted() != (i != 1) {
			t.Fatal("terms crossed configurations", name)
		}
		id, err := c.proxyIdentity(Settings{SOCKS5Address: "127.0.0.1:1080"})
		if err != nil || id != keys[name] {
			t.Fatal("identity lost after move", name, err)
		}
		if got, err := readSnapshot[int](c, "test"); err != nil || got != i+1 {
			t.Fatal("snapshot lost after move", got, err)
		}
		if i == 2 && c.settings.StorageDir != external {
			t.Fatal("external path changed")
		}
		c.Close()
	}
	if keys["config.json"] == keys["a.json"] || keys["a.json"] == keys["b.json"] {
		t.Fatal("shared channel identity")
	}
}
