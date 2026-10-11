package cacheproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemporaryCleanupRefusesOpenFile(t *testing.T) {
	cfg := fixtureConfig()
	cfg.StorageDir = t.TempDir()
	path := filepath.Join(cfg.tempDir(), "download-123.part")
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
