package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingVideosRequireExplicitPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := checkVideoPolicy(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkVideoPolicy(dir, false); err != nil {
		t.Fatal("unknown file triggered adoption", err)
	}
	path := filepath.Join(dir, strings.Repeat("a", 32)+".mp4")
	if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkVideoPolicy(dir, false); err == nil {
		t.Fatal("default zero budget could silently evict existing files")
	}
	if err := checkVideoPolicy(dir, true); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "video" {
		t.Fatal("policy validation changed existing data", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := checkVideoPolicy(missing, true); err == nil {
		t.Fatal("explicit retention is not permission to recreate a lost root")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("lost root recreated", err)
	}
}
