package applog

import (
	"fmt"
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
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		w.Close()
		t.Fatal("expected one new segment", entries, err)
	}
	stamp, hasExtension := strings.CutSuffix(entries[0].Name(), ".log")
	parsed, err := time.Parse(segmentLayout, stamp)
	if !hasExtension || err != nil || parsed.Format(segmentLayout) != stamp {
		w.Close()
		t.Fatal("segment name is not a local calendar date", entries[0].Name(), err)
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
		if _, _, ok := segmentID(files[i].name); !ok {
			t.Fatal("segment is not named by local date", files[i].name)
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

func TestConcurrentTextRotation(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, 1024, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := New(w, Info)
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
			if !strings.Contains(line, " [INFO] entry ") || !strings.Contains(line, "worker=") || !strings.Contains(line, "number=") {
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
	var transitions []error
	w.SetFaultObserver(func(err error) { transitions = append(transitions, err) })
	if _, err := w.Write([]byte("old\n")); err != nil {
		t.Fatal(err)
	}
	// Preserve and reject an unexpected type under a managed filename.
	blocker := filepath.Join(dir, segmentName(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), 0))
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
	if len(transitions) != 2 || transitions[0] == nil || transitions[1] != nil {
		t.Fatalf("write failure/recovery transitions: %v", transitions)
	}
	files := testSegments(t, dir)
	if len(files) != 1 {
		t.Fatal("failed to recover retention", files)
	}
	b, err := os.ReadFile(filepath.Join(dir, files[0].name))
	if err != nil || string(b) != "new\n" {
		t.Fatal(string(b), err)
	}
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err == nil {
		t.Fatal("a new rotation failure was not detected")
	}
	if len(transitions) != 3 || transitions[2] == nil || reports != 1 {
		t.Fatalf("new incident or legacy once-only callback changed: %v %d", transitions, reports)
	}
}

func TestSegmentRetentionIncludesLegacyAndPreservesUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"app.log.3", "app.log.1", "app.log", "app.log.4", "notes.log", "2026-11-03T00-00-00.000000000Z.log"} {
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
	for _, name := range []string{"app.log", "app.log.4", "notes.log", "2026-11-03T00-00-00.000000000Z.log"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "old\n" {
			t.Fatal("modified a retained or unknown file", name, err)
		}
	}
}

func TestSegmentsDoNotOverwriteExistingDates(t *testing.T) {
	dir := t.TempDir()
	// An existing full date segment must never be overwritten.
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	name := segmentName(at, 0)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("full"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := openWriter(dir, 4, 8, nil, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	files := testSegments(t, dir)
	if len(files) != 2 || files[0].name != name || files[1].name != segmentName(at, 1) {
		t.Fatal("segment names lost creation order", files)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(data) != "full" {
		t.Fatal("overwrote a previous segment", err)
	}
}

func TestDateSegmentsPreserveNumericOrderAndRestart(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	stamp := at.Format(segmentLayout)
	if err := os.WriteFile(filepath.Join(dir, stamp+".log"), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := openWriter(dir, 4, 48, nil, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 14; i++ {
		if _, err := w.Write([]byte(fmt.Sprintf("%03d\n", i))); err != nil {
			w.Close()
			t.Fatal(err)
		}
	}
	files := testSegments(t, dir)
	if len(files) != 12 {
		w.Close()
		t.Fatalf("wrong retained segment count: %d", len(files))
	}
	for i, file := range files {
		want := fmt.Sprintf("%s-%d.log", stamp, i+3)
		data, err := os.ReadFile(filepath.Join(dir, file.name))
		if file.name != want || err != nil || string(data) != fmt.Sprintf("%03d\n", i+3) {
			w.Close()
			t.Fatalf("retained segment %d: %s %q %v, want %s", i, file.name, data, err, want)
		}
	}
	if _, err := w.Write([]byte("x")); err != nil {
		w.Close()
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	last := fmt.Sprintf("%s-15.log", stamp)
	w, err = openWriter(dir, 4, 48, nil, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	files = testSegments(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, last))
	if len(files) != 12 || files[len(files)-1].name != last || err != nil || string(data) != "xy" {
		t.Fatalf("restart did not reuse the newest segment: %q %v, %v", data, err, files)
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

func TestLocalMidnightAndClockRollback(t *testing.T) {
	dir := t.TempDir()
	zone := time.FixedZone("local", 8*60*60)
	at := time.Date(2026, 11, 13, 23, 59, 59, 0, zone)
	w, err := openWriter(dir, 8, 80, nil, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, step := range []struct {
		delta time.Duration
		text  string
	}{
		{0, "one\n"},
		{time.Second, "two\n"}, // Local midnight is still the previous UTC date.
		{-time.Hour, "old\n"},  // Reuse that day's unfinished segment after rollback.
		{0, "next"},            // That segment is now full: allocate its next sequence.
		{2 * time.Hour, "new\n"},
	} {
		at = at.Add(step.delta)
		if _, err := w.Write([]byte(step.text)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"20261113.log":   "one\nold\n",
		"20261113-1.log": "next",
		"20261114.log":   "two\nnew\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v, want %q", name, data, err, want)
		}
	}
	if len(testSegments(t, dir)) != 3 {
		t.Fatal("unexpected segment after date changes")
	}
}

func TestUpgradeKeepsFormatsSeparateAndCleansHistoryFirst(t *testing.T) {
	dir := t.TempDir()
	seeds := map[string]string{
		"app.log":              "old!",
		"20261113T200000Z.log": "json",
		"20261113.log":         "now!",
		"20261114.log":         "next",
		"notes.log":            "user file",
	}
	for name, content := range seeds {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	open := func() *Writer {
		w, err := openWriter(dir, 8, 24, nil, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := open()
	if _, err := w.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.log", "20261113T200000Z.log", "20261114.log", "notes.log"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != seeds[name] {
			t.Fatalf("restart changed %s: %q %v", name, data, err)
		}
	}
	w = open() // Full current-day file, even though a future date exists.
	defer w.Close()
	for _, value := range []string{"eight888", "another8"} {
		if _, err := w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"app.log", "20261113T200000Z.log", "20261113.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("retention should remove %s first: %v", name, err)
		}
	}
	for _, name := range []string{"20261113-1.log", "20261113-2.log", "20261114.log", "notes.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("retention removed protected/newer content", name, err)
		}
	}
}
