package cacheproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupCleansOnlyRecognizedTemporaryMedia(t *testing.T) {
	cfg := fixtureConfig()
	cfg.StorageDir = t.TempDir()
	initial, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	removed := []string{"download-block-789.part", "download-123.part", "download-publish-456.part", "import-789/video"}
	kept := []string{"download-block-unknown.part", "download-not-ours.part", "import-not-ours/video", "import-111/video", "import-111/notes", "import-222/nested/video", "notes"}
	for _, name := range append(append([]string{}, removed...), kept...) {
		writeTestFile(t, filepath.Join(cfg.tempDir(), name), "keep content")
	}
	if err := os.Mkdir(filepath.Join(cfg.tempDir(), "import-333"), 0700); err != nil {
		t.Fatal(err)
	}
	video := cfg.videoFile(strings.Repeat("a", 32))
	writeTestFile(t, video, "complete video")
	// A staged import may be a link to both the source and a published video.
	if err := os.Mkdir(filepath.Join(cfg.tempDir(), "import-444"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(video, filepath.Join(cfg.tempDir(), "import-444/video")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range append(removed, "import-789", "import-333", "import-444") {
		if _, err := os.Lstat(filepath.Join(cfg.tempDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("not removed %s: %v", name, err)
		}
	}
	for _, name := range kept {
		if b, err := os.ReadFile(filepath.Join(cfg.tempDir(), name)); err != nil || string(b) != "keep content" {
			t.Fatalf("changed %s: %v", name, err)
		}
	}
	if b, err := os.ReadFile(video); err != nil || string(b) != "complete video" {
		t.Fatal("published video changed", err)
	}
}

func TestStartupLockConflictDoesNotCleanTemporaryMedia(t *testing.T) {
	cfg := fixtureConfig()
	cfg.StorageDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(cfg.tempDir(), "download-123.part")
	writeTestFile(t, path, "active")
	if other, err := New(cfg); err == nil {
		other.Close()
		t.Fatal("second owner started")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "active" {
		t.Fatal("active file changed", err)
	}
}

func TestTemporaryCleanupPreservesLinks(t *testing.T) {
	cfg := fixtureConfig()
	cfg.StorageDir = t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "video")
	writeTestFile(t, target, "outside")
	if err := os.MkdirAll(cfg.tempDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.tempDir(), "import-123")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(cfg.tempDir(), "download-456.part")); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, name := range []string{"import-123", "download-456.part"} {
		if _, err := os.Lstat(filepath.Join(cfg.tempDir(), name)); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "outside" {
		t.Fatal("link target changed", err)
	}
}
