package cacheproxy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

func TestBlockFilesCompleteBlocksAndIndependentLifetime(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(rangeBlockSize+19))
	f := newBlockFiles(t.TempDir(), int64(len(body)), rangeBlockSize, nil)
	var diskBytes int64
	f.onDiskChange = func(delta int64) { diskBytes += delta }
	defer f.release() // Engine ownership.
	f.retain()        // One execution/reader, independent of the engine.
	if err := f.store(0, body[:42]); err == nil || f.has(0) {
		t.Fatal("partial block became reusable", err)
	}
	if err := f.store(1, body[rangeBlockSize:]); err != nil {
		t.Fatal(err)
	}
	if f.complete() || f.available(0) != 0 || f.available(rangeBlockSize) != int64(len(body)) {
		t.Fatal("sparse availability was inferred from length")
	}
	if _, err := f.snapshot(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete merge snapshot", err)
	}
	if err := f.store(0, body[:rangeBlockSize]); err != nil {
		t.Fatal(err)
	}
	f.release() // Ending old execution must not remove complete reusable data.
	got, err := io.ReadAll(io.NewSectionReader(f, rangeBlockSize-7, 26))
	if err != nil || !bytes.Equal(got, body[rangeBlockSize-7:]) || !f.complete() {
		t.Fatal("cross-block reuse failed", err)
	}
	if err := f.store(0, body[:rangeBlockSize]); err != nil {
		t.Fatal(err)
	}
	if f.bytes() != int64(len(body)) || diskBytes != int64(len(body)) {
		t.Fatal("shared block counted twice or wrong final-block length", f.bytes(), diskBytes)
	}
}

func TestBlockFilesReclamationCannotDeleteNewBlockOrPinnedSnapshot(t *testing.T) {
	f := newBlockFiles(t.TempDir(), 8, rangeBlockSize, nil)
	var diskBytes int64
	f.onDiskChange = func(delta int64) { diskBytes += delta }
	defer f.release()
	if err := f.store(0, []byte("original")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	path := snapshot.blocks[0].path
	removed := f.retire(func(int64) bool { return true })
	if len(removed) != 0 || f.has(0) {
		t.Fatal("pinned block removed or still offered to new readers")
	}
	if err := f.store(0, []byte("new-data")); err != nil {
		t.Fatal(err)
	}
	f.removeFiles(removed)
	if f.bytes() != 8 || diskBytes != 16 {
		t.Fatal("retired pinned file stopped contributing to disk usage", f.bytes(), diskBytes)
	}
	old, err := io.ReadAll(io.NewSectionReader(snapshot, 0, 8))
	if err != nil || string(old) != "original" {
		t.Fatal("retired snapshot lost bytes", err)
	}
	snapshot.Close()
	snapshot.Close()
	if diskBytes != 8 {
		t.Fatal("closing snapshot did not release exactly its disk charge", diskBytes)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("retired block remained after final I/O reference", err)
	}
	got, err := io.ReadAll(io.NewSectionReader(f, 0, 8))
	if err != nil || string(got) != "new-data" {
		t.Fatal("stale cleanup deleted replacement", err)
	}
}

func TestBlockFilesReclaimsOnlySelectedBlocksAndCleansOnExit(t *testing.T) {
	f := newBlockFiles(t.TempDir(), rangeBlockSize+3, rangeBlockSize, nil)
	var diskBytes int64
	f.onDiskChange = func(delta int64) { diskBytes += delta }
	if err := f.store(0, make([]byte, rangeBlockSize)); err != nil {
		t.Fatal(err)
	}
	if err := f.store(1, []byte("end")); err != nil {
		t.Fatal(err)
	}
	f.removeFiles(f.retire(func(block int64) bool { return block == 0 }))
	if f.has(0) || !f.has(1) || f.bytes() != 3 || diskBytes != 3 {
		t.Fatal("block reclamation retained unrelated bytes")
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("physical block not reclaimed", len(entries), err)
	}
	f.release()
	if diskBytes != 0 {
		t.Fatal("exit retained disk charge", diskBytes)
	}
	entries, err = os.ReadDir(f.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("exit left temporary media", len(entries), err)
	}
	if err := f.store(1, []byte("end")); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed resource accepted writer", err)
	}
}

func TestBlockCacheDeletionFailureKeepsDiskCharge(t *testing.T) {
	f := newBlockFiles(t.TempDir(), 8, 8, nil)
	defer f.release()
	var diskBytes int64
	f.onDiskChange = func(delta int64) { diskBytes += delta }
	if err := f.store(0, []byte("original")); err != nil {
		t.Fatal(err)
	}
	files := f.retire(func(int64) bool { return true })
	path := files[0].path
	// A nonempty directory gives a deterministic removal failure on all OSes.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"/busy", nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.removeFiles(files)
	if diskBytes != 8 || f.bytes() != 0 {
		t.Fatal("failed deletion reported freed disk space", diskBytes)
	}
	if err := os.Remove(path + "/busy"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	f.removeFiles(files)
	if diskBytes != 0 {
		t.Fatal("successful deletion retained disk charge", diskBytes)
	}
}
