package applog

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSegments(t *testing.T, dir string) []segment {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, err := logSegments(root)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRotationAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := Open(dir, 8, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"111\n", "222\n", "333\n", "444\n", "555\n", "666\n", "777\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files := testSegments(t, dir)
	if len(files) != 3 {
		t.Fatal("wrong retention", files)
	}
	for i, want := range []string{"333\n444\n", "555\n666\n", "777\n"} {
		if _, ok := segmentTime(files[i].name); !ok {
			t.Fatal("segment is not named by creation time", files[i].name)
		}
		b, err := os.ReadFile(filepath.Join(dir, files[i].name))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", files[i].name, b, err)
		}
	}
	w, err = Open(dir, 8, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("888\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, files[2].name))
	if err != nil || string(b) != "777\n888\n" || len(testSegments(t, dir)) != 3 {
		t.Fatalf("restart did not append to the unfinished segment: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.log")); !os.IsNotExist(err) {
		t.Fatal("created an app.log anchor", err)
	}
}

func TestConcurrentJSONRotation(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 1024, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(w, nil))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				logger.Info("entry", "worker", id, "number", j)
			}
		}(i)
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var total int
	for _, file := range testSegments(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil || len(b) == 0 || len(b) > 1024 {
			t.Fatalf("size=%d err=%v", len(b), err)
		}
		total += len(b)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if !json.Valid([]byte(line)) {
				t.Fatalf("torn record: %q", line)
			}
		}
	}
	if total > 4096 {
		t.Fatalf("retained %d bytes beyond the budget", total)
	}
}

func TestRotationFailureReportedOnceAndRecovers(t *testing.T) {
	dir := t.TempDir()
	reports := 0
	w, err := Open(dir, 4, 4, func(error) { reports++ })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("old\n")); err != nil {
		t.Fatal(err)
	}
	// Preserve and reject an unexpected type under a managed filename.
	blocker := filepath.Join(dir, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(segmentLayout))
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := w.Write([]byte("new\n")); err == nil {
			t.Fatal("rotation should fail")
		}
	}
	if reports != 1 {
		t.Fatal(reports)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	files := testSegments(t, dir)
	if len(files) != 1 {
		t.Fatal("failed to recover retention", files)
	}
	b, err := os.ReadFile(filepath.Join(dir, files[0].name))
	if err != nil || string(b) != "new\n" {
		t.Fatal(string(b), err)
	}
}

func TestSegmentRetentionIncludesLegacyAndPreservesUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"app.log.3", "app.log.1", "app.log", "app.log.4", "notes.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w, err := Open(dir, 8, 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.log.3", "app.log.1"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("oldest legacy file was not reclaimed", name, err)
		}
	}
	for _, name := range []string{"app.log", "app.log.4", "notes.log"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "old\n" {
			t.Fatal("modified a retained or unknown file", name, err)
		}
	}
}

func TestSegmentsDoNotOverwriteExistingTimestamps(t *testing.T) {
	dir := t.TempDir()
	// Future filenames also exercise clock rollback across restarts.
	at := time.Now().UTC().Add(time.Hour)
	name := at.Format(segmentLayout)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("full"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(dir, 4, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	files := testSegments(t, dir)
	if len(files) != 2 || files[0].name != name || files[1].name <= name {
		t.Fatal("segment names lost creation order", files)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(data) != "full" {
		t.Fatal("overwrote a previous segment", err)
	}
}

func TestSegmentRejectsOversizedRecordsWithoutSplitting(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 4, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("large")); err == nil {
		t.Fatal("accepted a record larger than a segment")
	}
	if _, err := w.Write([]byte("fits")); err != nil {
		t.Fatal(err)
	}
	files := testSegments(t, dir)
	if len(files) != 1 {
		t.Fatal("oversized record changed the files", files)
	}
	data, err := os.ReadFile(filepath.Join(dir, files[0].name))
	if err != nil || string(data) != "fits" {
		t.Fatal("oversized record changed the contents", string(data), err)
	}
}
