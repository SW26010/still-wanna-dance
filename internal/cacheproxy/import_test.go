package cacheproxy

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestImportCrossVolumeCopy(t *testing.T) {
	root := os.Getenv("SWD_IMPORT_TEST_VOLUME")
	if root == "" {
		t.Skip("set SWD_IMPORT_TEST_VOLUME to a writable directory on a second volume")
	}
	sourceDir, err := os.MkdirTemp(root, "swd-import-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sourceDir)
	s, _ := setup(t, nil)
	data := []byte("cross volume video")
	key := fmt.Sprintf("%x", md5.Sum(data))
	source := filepath.Join(sourceDir, "video.mp4")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.ImportVideo(context.Background(), source, map[string]bool{key: true})
	if err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	a, _ := os.Stat(source)
	b, _ := os.Stat(receipt.Destination)
	if os.SameFile(a, b) {
		t.Fatal("expected separate cross-volume copy")
	}
	if err := s.RemoveImportedSource(context.Background(), *receipt); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(receipt.Destination)
	if err != nil || string(got) != string(data) {
		t.Fatal("copy damaged", err)
	}
}

func TestImportVideoLinkSkipAndCleanup(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	data := []byte("external video content")
	key := fmt.Sprintf("%x", md5.Sum(data))
	source := filepath.Join(t.TempDir(), "renamed.MP4")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := s.ImportVideo(ctx, source, map[string]bool{}); err != nil || receipt != nil {
		t.Fatal(receipt, err)
	}
	receipt, err := s.ImportVideo(ctx, source, map[string]bool{key: true})
	if err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	a, _ := os.Stat(source)
	b, err := os.Stat(receipt.Destination)
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("expected same-volume hard link", err)
	}
	if second, err := s.ImportVideo(ctx, source, map[string]bool{key: true}); err != nil || second != nil {
		t.Fatal("duplicate imported", second, err)
	}
	if err := s.RemoveImportedSource(ctx, *receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("source remains", err)
	}
	got, err := os.ReadFile(receipt.Destination)
	if err != nil || string(got) != string(data) {
		t.Fatal("destination lost", err)
	}
}

func TestImportCleanupRejectsChangedSourceOrMissingDestination(t *testing.T) {
	for _, kind := range []string{"replaced", "modified", "missing-destination"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := setup(t, nil)
			ctx := context.Background()
			data := []byte("original video")
			key := fmt.Sprintf("%x", md5.Sum(data))
			source := filepath.Join(t.TempDir(), "video.mp4")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			receipt, err := s.ImportVideo(ctx, source, map[string]bool{key: true})
			if err != nil || receipt == nil {
				t.Fatal(receipt, err)
			}
			switch kind {
			case "replaced":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "modified":
				if err := os.WriteFile(source, []byte("changed video!"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-destination":
				if err := os.Remove(receipt.Destination); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RemoveImportedSource(ctx, *receipt); err == nil {
				t.Fatal("unsafe deletion allowed")
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("source removed", err)
			}
		})
	}
}

func TestImportCancelledAndMissingSources(t *testing.T) {
	s, _ := setup(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(source, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := s.ImportVideo(ctx, source, nil); err == nil || receipt != nil {
		t.Fatal("canceled import succeeded")
	}
	if receipt, err := s.ImportVideo(context.Background(), source+".moved", nil); err == nil || receipt != nil {
		t.Fatal("missing import succeeded")
	}
}
