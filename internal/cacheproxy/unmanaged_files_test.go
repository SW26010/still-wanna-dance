package cacheproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageLeavesUnmanagedFilesUntouched(t *testing.T) {
	root := t.TempDir()
	files := []string{"stepstash.sqlite", "stepstash.sqlite-journal", filepath.Join("videos", strings.Repeat("a", 64)+".mp4")}
	for _, name := range files {
		writeTestFile(t, filepath.Join(root, name), "unmanaged data")
	}
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, root, true)
	cfg.VideoBudgetBytes, cfg.CacheUnlimited = 0, false
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if b, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(b) != "unmanaged data" {
			t.Fatal("unmanaged file changed", name, err)
		}
	}
}
