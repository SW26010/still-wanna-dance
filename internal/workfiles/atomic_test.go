package workfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteRetainsLastGoodFileAndBoundsWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Write(path, []byte("good")); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(path), TempName(path))
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(tmp, []byte("interrupted"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Write(path, []byte("replacement")); err == nil {
			t.Fatal("overwrote occupied work slot")
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "good" {
			t.Fatal("lost last good file", err)
		}
		if removed, err := Clean(path); err != nil || !removed {
			t.Fatal(removed, err)
		}
		if err := Write(path, []byte("good")); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("unbounded work files", entries, err)
	}
}

func TestAtomicWriteNeverTruncatesLinkedWorkFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "source")
	if err := os.WriteFile(target, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Link(target, filepath.Join(dir, TempName(path))); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new")); err == nil {
		t.Fatal("opened linked work file")
	}
	if removed, err := Clean(path); err != nil || !removed {
		t.Fatal(removed, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "protected" {
		t.Fatal("modified hardlink content", err)
	}
}

func TestAtomicWorkDirectoryIsNotRecursivelyDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	dir := filepath.Join(filepath.Dir(path), TempName(path))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if removed, err := Clean(path); err == nil || removed {
		t.Fatal("accepted directory", removed, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

func TestProbeLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := Probe(dir); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
