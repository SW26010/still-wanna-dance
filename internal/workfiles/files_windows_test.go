package workfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCleanupRejectsWindowsJunction(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "state.json")
	tmp := filepath.Join(outside, TempName(path))
	if err := os.WriteFile(tmp, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v: %s", err, out)
	}
	if _, err := Clean(filepath.Join(link, "state.json")); err == nil {
		t.Fatal("followed junction")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := RemoveFile(root, filepath.Join("state", TempName(path))); err == nil {
		t.Fatal("relative deletion followed junction")
	}
	if b, err := os.ReadFile(tmp); err != nil || string(b) != "external" {
		t.Fatal("external file changed", err)
	}
}

func TestCleanupReportsReadOnlyWorkFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	tmp := filepath.Join(filepath.Dir(path), TempName(path))
	if err := os.WriteFile(tmp, []byte("interrupted"), 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tmp, 0600)
	if removed, err := Clean(path); err == nil || removed {
		t.Fatal("ignored read-only failure", removed, err)
	}
	if b, err := os.ReadFile(tmp); err != nil || string(b) != "interrupted" {
		t.Fatal("read-only file changed", err)
	}
}
