package cacheproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTemporaryCleanupPreservesOpenFilesAndStartsEngine(t *testing.T) {
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	path := filepath.Join(cfg.publishDir(), "download-publish-123.part")
	writeTestFile(t, path, "active")
	imported := filepath.Join(cfg.publishDir(), "import-123", "video")
	writeTestFile(t, imported, "imported")
	abandoned := filepath.Join(cfg.publishDir(), "download-publish-456.part")
	writeTestFile(t, abandoned, "abandoned")
	hold := func(path string) *os.File {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	f, importedFile := hold(path), hold(imported)
	s, err := New(cfg)
	if err != nil {
		t.Fatal("open temporary file prevented engine startup", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "active" {
		t.Fatal("open file changed", err)
	}
	if b, err := os.ReadFile(imported); err != nil || string(b) != "imported" {
		t.Fatal("open import changed", err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatal("other temporary file was not collected", err)
	}
	f.Close()
	importedFile.Close()
	s, err = New(cfg)
	if err != nil {
		t.Fatal("reopening engine after releasing files", err)
	}
	defer s.Close()
	for _, cleaned := range []string{path, filepath.Dir(imported)} {
		if _, err := os.Stat(cleaned); !os.IsNotExist(err) {
			t.Fatal("released temporary file was not collected", cleaned, err)
		}
	}
}

func TestBlockWorkspaceContinuesAfterSkippingOpenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download-block-123.part")
	writeTestFile(t, path, "active")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	work, err := OpenWorkDirectory(dir, nil)
	if err != nil {
		t.Fatal("open temporary file prevented workspace startup", err)
	}
	if work.CleanupPending() || work.CleanupComplete() {
		t.Fatal("workspace was blocked or reported fully cleaned")
	}
	if err := work.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CleanExistingWorkDirectory(dir, nil); err == nil {
		t.Fatal("a recorded old location would be cleared before cleanup succeeds")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "active" {
		t.Fatal("open block changed", err)
	}
	f.Close()
	if err := CleanExistingWorkDirectory(dir, nil); err != nil {
		t.Fatal(err)
	}
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
