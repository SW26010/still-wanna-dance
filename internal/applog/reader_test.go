package applog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func logRecord(t *testing.T, at time.Time, level slog.Level, message string, attrs ...any) []byte {
	t.Helper()
	var output bytes.Buffer
	record := slog.NewRecord(at, level, message, 0)
	record.Add(attrs...)
	if err := slog.NewJSONHandler(&output, nil).Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestReadLogsFiltersRotationsAndPreservesFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	at := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	for _, file := range []struct {
		name, message string
		at            time.Time
		level         slog.Level
	}{
		{path + ".3", "later_timestamp", at.Add(4 * time.Second), slog.LevelInfo},
		{path + ".1", "older_warn", at, slog.LevelWarn},
		{path, "selected_warn", at.Add(time.Second), slog.LevelWarn},
		{path + ".4", "outside_retention", at.Add(time.Hour), slog.LevelError},
	} {
		data := logRecord(t, file.at, file.level, file.message, "song_ids", []int{1343, -1}, "precise", int64(9007199254740993), "error", "line one\nnot_a_key=inside value\n<script>literal</script>")
		if err := os.WriteFile(file.name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Read(t.Context(), path, Query{From: at.Add(time.Second), To: at.Add(4 * time.Second), Level: "WARN"})
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Message != "selected_warn" || result.Matches != 1 || len(result.Files) != 3 {
		t.Fatalf("filtered snapshot: %+v, %v", result, err)
	}
	entry := result.Entries[0]
	for _, field := range []Field{{Key: "song_ids", Value: "[1343,-1]"}, {Key: "precise", Value: "9007199254740993"}, {Key: "error", Value: "line one\nnot_a_key=inside value\n<script>literal</script>"}} {
		if !slices.Contains(entry.Fields, field) {
			t.Fatalf("lost field %+v: %+v", field, entry.Fields)
		}
	}
	if entry.File != "app.log" || entry.Line != 1 || !strings.Contains(entry.Raw, `"precise":9007199254740993`) {
		t.Fatal("missing original record", entry)
	}
	all, err := Read(t.Context(), path, Query{})
	if err != nil || len(all.Entries) != 3 || all.Entries[0].Message != "later_timestamp" || all.Entries[2].Message != "older_warn" {
		t.Fatalf("global time ordering: %+v, %v", all, err)
	}
	for _, tc := range []struct {
		keyword string
		count   int
	}{
		{"older_warn", 1},
		{"DER_W", 1},
		{" WARN ", 2},
		{"  ", 3},
		{"warn.*", 0},
		{"<script>", 0}, // Search event names, not field values.
	} {
		selected, err := Read(t.Context(), path, Query{Event: tc.keyword})
		if err != nil || selected.Matches != tc.count {
			t.Fatalf("event keyword %q: matches=%d, %v", tc.keyword, selected.Matches, err)
		}
	}
	for _, level := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		filtered, err := Read(t.Context(), path, Query{Level: level})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range filtered.Entries {
			if entry.Level != level {
				t.Fatalf("level filter behaved as a threshold: %+v", entry)
			}
		}
	}
}

func TestReadLogsBoundsResultsAndReportsUnreadableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	var data bytes.Buffer
	at := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	for i := 0; i < ReadLimit+3; i++ {
		data.Write(logRecord(t, at, slog.LevelInfo, fmt.Sprintf("event_%d", i)))
	}
	data.WriteString("broken\n{\"time\":")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Read(t.Context(), path, Query{})
	if err != nil || result.Matches != ReadLimit+3 || len(result.Entries) != ReadLimit || result.Skipped != 2 {
		t.Fatalf("bounded snapshot: %+v, %v", result, err)
	}
	if result.Entries[0].Message != fmt.Sprintf("event_%d", ReadLimit+2) || result.Entries[ReadLimit-1].Message != "event_3" {
		t.Fatal("equal timestamps did not preserve newest write order")
	}
	matched, err := Read(t.Context(), path, Query{Event: "event_0"})
	if err != nil || matched.Matches != 1 || len(matched.Entries) != 1 || matched.Entries[0].Message != "event_0" {
		t.Fatal("event keyword only searched displayed records", matched, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data.Bytes(), after) {
		t.Fatal("reader modified original evidence", err)
	}
	// Real stack traces can exceed bufio.Scanner's default token limit.
	large := logRecord(t, at, slog.LevelError, "large_error", "stack", strings.Repeat("frame\n", 15000))
	if err := os.WriteFile(path, large, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = Read(t.Context(), path, Query{})
	if err != nil || len(result.Entries) != 1 || result.Skipped != 0 || !slices.Contains(result.Entries[0].Fields, Field{Key: "stack", Value: strings.Repeat("frame\n", 15000)}) {
		t.Fatal("large record not readable", err)
	}
}

func TestExportLogsTextIncludesAllMatchesAndUsesEventKeyword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	at := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	var source bytes.Buffer
	for i := 0; i < ReadLimit+3; i++ {
		record := logRecord(t, at.Add(time.Duration(i)*time.Second), slog.LevelInfo, fmt.Sprintf("export_%d", i), "precise", int64(9007199254740993), "song_ids", []int{1343, -1})
		source.Write(record)
	}
	source.WriteString("invalid\n{\"unfinished\":")
	if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Export(t.Context(), path, Query{})
	if err != nil || result.Matches != ReadLimit+3 || result.Skipped != 2 || strings.Count(string(result.Data), "[INFO] export_") != ReadLimit+3 || !strings.Contains(string(result.Data), "[INFO] export_0\n") || !strings.Contains(string(result.Data), "[INFO] export_502\n") {
		t.Fatalf("text export lost records: matches=%d skipped=%d err=%v", result.Matches, result.Skipped, err)
	}
	filter := Query{From: at.Add(time.Second), To: at.Add(3 * time.Second), Level: "INFO", Event: " PoRt_2 "}
	result, err = Export(t.Context(), path, filter)
	if err != nil || result.Matches != 1 || !strings.Contains(string(result.Data), "[INFO] export_2\n") || strings.Contains(string(result.Data), "[INFO] export_1\n") || strings.Contains(string(result.Data), "[INFO] export_3\n") || !strings.Contains(string(result.Data), "precise=9007199254740993\nsong_ids=[1343,-1]\n") {
		t.Fatalf("filtered text export: %s, %v", result.Data, err)
	}
	result, err = Export(t.Context(), path, Query{Event: "PORT_"})
	if err != nil || result.Matches != ReadLimit+3 {
		t.Fatal("keyword export lost multiple matching events", result.Matches, err)
	}
	result, err = Export(t.Context(), path, Query{Level: "ERROR"})
	if err != nil || result.Matches != 0 || len(result.Data) != 0 {
		t.Fatal("empty export", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source.Bytes(), after) {
		t.Fatal("export changed source log", err)
	}
}

func TestReadLogsDoesNotCreateFilesAndRejectsInvalidReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "app.log")
	result, err := Read(t.Context(), path, Query{})
	if err != nil || len(result.Files) != 0 || len(result.Entries) != 0 {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader created logs directory", err)
	}
	for _, query := range []Query{{Level: "off"}, {Level: "nonsense"}, {From: time.Now(), To: time.Unix(0, 0)}} {
		if _, err := Read(t.Context(), path, query); err == nil {
			t.Fatal("accepted invalid query", query)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Read(ctx, path, Query{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read continued", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), path, Query{}); err == nil {
		t.Fatal("read an oversized file")
	}
}

func TestLogSnapshotSurvivesWriterRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	writer, err := Open(path, 160, Backups, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	at := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	deletedOldestWhileOpen := false
	for i := 0; i < 12; i++ {
		// Hold the reader's real handles across rotation, including deletion of
		// the oldest backup. On Windows these handles must share delete access.
		snapshot, err := openSnapshot(root, filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		var oldest *logFile
		for index := range snapshot {
			file := &snapshot[index]
			if file.name == fmt.Sprintf("%s.%d", filepath.Base(path), Backups) && file.file != nil {
				oldest = file
			}
		}
		var original []byte
		if oldest != nil {
			original = make([]byte, oldest.info.Size())
			if _, err := oldest.file.ReadAt(original, 0); err != nil {
				closeLogs(snapshot)
				t.Fatal(err)
			}
		}
		if _, err := writer.Write(logRecord(t, at.Add(time.Duration(i)*time.Second), slog.LevelInfo, fmt.Sprintf("event_%d", i))); err != nil {
			closeLogs(snapshot)
			t.Fatal(err)
		}
		if oldest != nil {
			replacement, err := root.Stat(oldest.name)
			if err != nil {
				closeLogs(snapshot)
				t.Fatal(err)
			}
			if !os.SameFile(oldest.info, replacement) {
				held := make([]byte, oldest.info.Size())
				_, err := oldest.file.ReadAt(held, 0)
				if err != nil || !bytes.Equal(original, held) {
					closeLogs(snapshot)
					t.Fatal("deleted oldest backup is not readable through its snapshot handle", err)
				}
				deletedOldestWhileOpen = true
				t.Logf("oldest backup replaced with its snapshot handle held open on write %d", i+1)
			}
		}
		closeLogs(snapshot)
		result, err := Read(t.Context(), path, Query{})
		if err != nil || len(result.Entries) == 0 || result.Entries[0].Message != fmt.Sprintf("event_%d", i) || len(result.Files) > Backups+1 {
			t.Fatalf("live writer snapshot: %+v, %v", result, err)
		}
	}
	if !deletedOldestWhileOpen {
		t.Fatal("test never replaced an existing oldest backup while its snapshot handle was open")
	}
}
