package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func importReadBytes(t *testing.T) uint64 {
	t.Helper()
	var counters windows.IO_COUNTERS
	ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters").Call(
		uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&counters)))
	if ok == 0 {
		t.Fatal(err)
	}
	return counters.ReadTransferCount
}

func TestImportHardLinkReadVolume(t *testing.T) {
	s, _ := setup(t, nil)
	data := bytes.Repeat([]byte("video"), 1<<20) // 5 MiB
	key := fmt.Sprintf("%x", md5.Sum(data))
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := importReadBytes(t)
	receipt, err := s.ImportVideo(context.Background(), source, map[string]bool{key: true})
	if err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	imported := importReadBytes(t)
	a, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(receipt.Destination)
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("expected same-volume hard link", err)
	}
	beforeCleanup := importReadBytes(t)
	if err := s.RemoveImportedSource(context.Background(), *receipt); err != nil {
		t.Fatal(err)
	}
	cleaned := importReadBytes(t)
	// Count actual process reads, allowing small unrelated reads (e.g. SQLite)
	// while rejecting a second full pass over the video in either phase.
	for _, phase := range []struct {
		name string
		read uint64
	}{
		{"import", imported - before},
		{"cleanup", cleaned - beforeCleanup},
	} {
		t.Logf("%s read %d bytes for %d-byte video", phase.name, phase.read, len(data))
		if phase.read < uint64(len(data)) || phase.read > uint64(len(data)+len(data)/2) {
			t.Errorf("%s must read the shared content once, got %d bytes", phase.name, phase.read)
		}
	}
}
