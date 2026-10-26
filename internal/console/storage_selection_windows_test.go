package console

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestMissingStorageRejectsExternalJunctionAncestor(t *testing.T) {
	c := testConsole(t)
	root := t.TempDir()
	target, alias := filepath.Join(root, "external"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction: %s: %v", output, err)
	}
	path := filepath.Join(alias, "missing-parent", "media")
	before, _ := os.ReadFile(c.configPath)
	if w := storageRequest(t, c, "create-database", fixtureLibraryPaths(path)); w.Code != 400 {
		t.Fatal("explicit creation accepted junction ancestor", w.Code)
	}
	if _, err := cacheproxy.LoadCatalogStatus(context.Background(), fixtureLibraryPaths(path).DatabasePath); err == nil {
		t.Fatal("console inspection accepted junction ancestor")
	}
	after, _ := os.ReadFile(c.configPath)
	entries, err := os.ReadDir(target)
	if !bytes.Equal(before, after) || err != nil || len(entries) != 0 {
		t.Fatal("rejection changed configuration or target", err)
	}
}

func TestStorageJunctionCannotEnterPortable(t *testing.T) {
	root := t.TempDir()
	portable, alias := filepath.Join(root, "Portable"), filepath.Join(root, "external-alias")
	if err := os.Mkdir(portable, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, portable)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("junction: %s: %v", output, err)
	}
	if err := validateStorageLocation(filepath.Join(alias, "future", "library"), filepath.Join(portable, "data", "storage"), portable); err == nil {
		t.Fatal("junction bypassed custom storage boundary")
	}
	entries, err := os.ReadDir(portable)
	if err != nil || len(entries) != 0 {
		t.Fatalf("validation created media: %v %v", entries, err)
	}
}
