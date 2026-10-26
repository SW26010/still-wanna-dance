package cacheproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkDirectoryCleansBlocksWithoutDatabaseOrVideo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work", "instance")
	w, err := OpenWorkDirectory(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Path() != dir {
		t.Fatal(w.Path())
	}
	defer w.Close()
	block := filepath.Join(dir, "download-block-123.part")
	unknown := []string{"download-block-mine.part", "download-publish-456.part", "storage.sqlite", "notes.txt", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4"}
	for _, name := range append(unknown, filepath.Base(block)) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "import-123"), 0700); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenWorkDirectory(dir, nil); err == nil {
		other.Close()
		t.Fatal("second owner acquired active workspace")
	}
	if _, err := os.Stat(block); err != nil {
		t.Fatal("rejected owner cleaned active block", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = OpenWorkDirectory(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := os.Stat(block); !os.IsNotExist(err) {
		t.Fatal("owned block survived restart", err)
	}
	for _, name := range unknown {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "preserve" {
			t.Fatal("work cleanup touched another category", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "import-123")); err != nil || !info.IsDir() {
		t.Fatal("block cleanup touched import stage", err)
	}
}

func TestPublicationCleanupPreservesBlocksAndUnrecognizedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"download-block-123.part", "download-publish-123.part", "download-publish-mine.part"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cleanMediaWorkFiles(dir, fixtureConfig().Logger, publicationWork); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "download-publish-123.part")); !os.IsNotExist(err) {
		t.Fatal("publication stage survived", err)
	}
	for _, name := range []string{"download-block-123.part", "download-publish-mine.part"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("publication cleanup touched foreign work", err)
		}
	}
}

func TestEngineSeparatesBlocksFromPublicationAndImport(t *testing.T) {
	w, err := OpenWorkDirectory(filepath.Join(t.TempDir(), "work"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	independent := independentConfig(t)
	s, cfg := setup(t, nil, func(c *Config) {
		c.Work = w
		c.Store = independent.Store
		c.VideoDir = independent.VideoDir

	})
	f, v := completedRangeFlight(t, s, payload)
	blocks, err := filepath.Glob(filepath.Join(w.Path(), "download-block-*.part"))
	if err != nil || len(blocks) != 1 {
		t.Fatal("block did not use prepared workspace", blocks, err)
	}
	// Fail only the publication directory. Downloaded blocks in the separate
	// workspace must remain usable, and a repaired publication can reuse them.
	if err := os.Remove(cfg.publishDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.publishDir(), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.publishBlocks(t.Context(), cfg.videoFile(v.key), v, f); err == nil {
		t.Fatal("publication ignored its target-side stage")
	}
	if got, err := os.ReadFile(blocks[0]); err != nil || string(got) != payload {
		t.Fatal("publication failure lost the block", err)
	}
	if err := os.Remove(cfg.publishDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.publishDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.publishBlocks(t.Context(), cfg.videoFile(v.key), v, f); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || string(got) != payload {
		t.Fatal("published video", err)
	}
	source := filepath.Join(t.TempDir(), "source.mp4")
	const imported = "separate import source"
	if err := os.WriteFile(source, []byte(imported), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.ImportVideo(t.Context(), source, map[string]bool{videoKey(imported): true})
	if err != nil || receipt == nil {
		t.Fatal("import failed", err)
	}
	if got, err := os.ReadFile(receipt.Destination); err != nil || string(got) != imported {
		t.Fatal("imported video", err)
	}
	entries, err := os.ReadDir(cfg.publishDir())
	if err != nil || len(entries) != 0 {
		t.Fatal("publication/import left work behind", entries, err)
	}
	entries, err = os.ReadDir(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" && entry.Name() != filepath.Base(blocks[0]) {
			t.Fatal("non-block file in block workspace", entry.Name())
		}
	}
}
