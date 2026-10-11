package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if !samePath(path, relative) {
		t.Fatal("relative alias rejected")
	}
	if runtime.GOOS == "windows" && !samePath(path, strings.ToUpper(path)) {
		t.Fatal("case alias rejected")
	}
	link := filepath.Join(root, "alias.json")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if !samePath(path, link) {
		t.Fatal("hard link rejected")
	}
	copy := filepath.Join(root, "copy.json")
	if err := os.WriteFile(copy, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if samePath(path, copy) {
		t.Fatal("distinct file with identical contents accepted")
	}
	future := filepath.Join(root, "new", "data", "config.json")
	canonical, err := CanonicalPath(future)
	if err != nil || canonical != future {
		t.Fatal(canonical, err)
	}
	if _, err := os.Stat(filepath.Dir(future)); !os.IsNotExist(err) {
		t.Fatal("resolution created directories", err)
	}
	if samePath(path, future) || samePath("", "") {
		t.Fatal("unrelated identity accepted")
	}
}

func TestCanonicalPathThroughDirectoryAlias(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	a := filepath.Join(real, "data", "config.json")
	b := filepath.Join(alias, "data", "config.json")
	if !samePath(a, b) {
		t.Fatal("uncreated configuration under alias rejected")
	}
	first, err := LockConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := LockConfig(b); err == nil {
		second.Close()
		t.Fatal("alias bypassed config lock")
	}
}
