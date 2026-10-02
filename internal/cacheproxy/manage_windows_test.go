package cacheproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCacheDirectoryAcceptsWindowsShortPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache directory with a long name")
	if err := os.MkdirAll(filepath.Join(root, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	wide, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(wide, &buf[0], uint32(len(buf)))
	if err != nil || n >= uint32(len(buf)) {
		t.Fatalf("short path: length=%d err=%v", n, err)
	}
	short := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(short, root) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	if _, err := CacheDirectory(short); err != nil {
		t.Fatalf("ordinary directory through short path %q: %v", short, err)
	}
}

func TestCacheDirectoryRejectsWindowsJunctions(t *testing.T) {
	for _, component := range []string{"videos", "root", "ancestor"} {
		t.Run(component, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "target")
			if err := os.MkdirAll(filepath.Join(target, "cache", "videos"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(target, "videos"), 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, "link")
			root := link
			switch component {
			case "videos":
				root = base
				link = filepath.Join(base, "videos")
			case "ancestor":
				root = filepath.Join(link, "cache")
			}
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
				t.Fatalf("create test junction: %v: %s", err, out)
			}
			if _, err := CacheDirectory(root); err == nil {
				t.Fatal("accepted junction in cache path")
			}
		})
	}
}
