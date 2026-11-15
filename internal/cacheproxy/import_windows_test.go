package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestImportWithoutHardLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"same-volume", windows.ERROR_NOT_SUPPORTED},
		{"invalid-function", windows.ERROR_INVALID_FUNCTION},
		{"cross-volume", windows.ERROR_NOT_SAME_DEVICE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := setup(t, nil)
			data := []byte("video on a filesystem without hard links")
			key := fmt.Sprintf("%x", md5.Sum(data))
			source := filepath.Join(t.TempDir(), "video.mp4")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := SnapshotImportFile(source)
			if err != nil {
				t.Fatal(err)
			}
			link := func(from, to string) error {
				failure := error(windows.ERROR_NOT_SUPPORTED)
				if from == source {
					failure = tc.err
				}
				return &os.LinkError{Op: "link", Old: from, New: to, Err: failure}
			}
			receipt, err := s.importSnapshotVideo(context.Background(), snapshot, map[string]bool{key: true}, link)
			if err != nil || receipt == nil {
				t.Fatalf("import must work without hard links: receipt=%v error=%v", receipt, err)
			}
			a, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(receipt.Destination)
			if err != nil || os.SameFile(a, b) {
				t.Fatal("expected an independent copy", err)
			}
			if entries, err := os.ReadDir(cfg.publishDir()); err != nil || len(entries) != 0 {
				t.Fatal("staging files remain", entries, err)
			}
			if err := s.RemoveImportedSource(context.Background(), *receipt); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source remains", err)
			}
			if got, err := os.ReadFile(receipt.Destination); err != nil || !bytes.Equal(got, data) {
				t.Fatal("copy damaged", err)
			}
		})
	}
}

func TestImportCopyFallbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		linkErr error
		wantErr error
	}{
		{"access-denied", windows.ERROR_ACCESS_DENIED, windows.ERROR_ACCESS_DENIED},
		{"disk-full", windows.ERROR_DISK_FULL, windows.ERROR_DISK_FULL},
		{"cancel-copy", windows.ERROR_NOT_SUPPORTED, context.Canceled},
		{"destination-created-during-import", windows.ERROR_NOT_SUPPORTED, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := setup(t, nil)
			data := []byte("source video")
			key := fmt.Sprintf("%x", md5.Sum(data))
			source := filepath.Join(t.TempDir(), "video.mp4")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := SnapshotImportFile(source)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			link := func(from, to string) error {
				if tc.name == "cancel-copy" {
					cancel()
				}
				if tc.wantErr == nil {
					if err := os.WriteFile(cfg.videoFile(key), []byte("existing destination"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return &os.LinkError{Op: "link", Old: from, New: to, Err: tc.linkErr}
			}
			receipt, err := s.importSnapshotVideo(ctx, snapshot, map[string]bool{key: true}, link)
			if receipt != nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("receipt=%v error=%v, want no receipt and %v", receipt, err, tc.wantErr)
			}
			if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, data) {
				t.Fatal("source changed", err)
			}
			if tc.wantErr == nil {
				if got, err := os.ReadFile(cfg.videoFile(key)); err != nil || string(got) != "existing destination" {
					t.Fatal("concurrent destination overwritten", err)
				}
			} else if _, err := os.Stat(cfg.videoFile(key)); !os.IsNotExist(err) {
				t.Fatal("failed import published a destination", err)
			}
			if entries, err := os.ReadDir(cfg.publishDir()); err != nil || len(entries) != 0 {
				t.Fatal("staging files remain", entries, err)
			}
		})
	}
}

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
