package cacheproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTemporaryCleanupRefusesOpenFile(t *testing.T) {
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	path := filepath.Join(cfg.publishDir(), "download-publish-123.part")
	writeTestFile(t, path, "active")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := New(cfg); err == nil {
		s.Close()
		t.Fatal("cleanup ignored open handle")
	}
	f.Close()
	if b, err := os.ReadFile(path); err != nil || string(b) != "active" {
		t.Fatal("open file changed", err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal("failed startup leaked storage lock", err)
	}
	s.Close()
}

func TestTemporaryCleanupPreservesWindowsJunction(t *testing.T) {
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	outside := t.TempDir()
	target := filepath.Join(outside, "video")
	writeTestFile(t, target, "external")
	if err := os.MkdirAll(cfg.publishDir(), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(cfg.publishDir(), "import-123")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v: %s", err, out)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if b, err := os.ReadFile(target); err != nil || string(b) != "external" {
		t.Fatal("external file changed", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("junction removed", err)
	}
}
