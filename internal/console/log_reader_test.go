package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
)

func TestLogsAPIReadsWithoutEngineAndValidatesFilters(t *testing.T) {
	c := testConsole(t)
	path := filepath.Join(c.files().LogDir, "20261102T010000Z.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"time\":\"2026-11-02T02:00:00Z\",\"level\":\"WARN\",\"msg\":\"fixture_warning\",\"reason\":\"fixture\"}\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		code  int
		count int
	}{
		{"", 200, 1},
		{"?level=WARN&from=1793584800000&to=1793584801000", 200, 1},
		{"?level=ERROR", 200, 0},
		{"?event=fixture_warning", 200, 1},
		{"?event=fixture_warn", 200, 1},
		{"?event=%20TURE_WARN%20", 200, 1},
		{"?event=warning", 200, 1},
		{"?event=%20%20", 200, 1},
		{"?event=fixture.*", 200, 0},
		{"?event=missing", 200, 0},
		{"?from=not-a-time", 400, 0},
		{"?from=2&to=1", 400, 0},
		{"?level=off", 400, 0},
	} {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/logs"+tc.query, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("query %s: %d %s", tc.query, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var result applog.ReadResult
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Entries) != tc.count {
				t.Fatalf("query %s: %+v %v", tc.query, result, err)
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("private logs are cacheable")
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if _, present := payload["events"]; present {
				t.Fatal("obsolete event enumeration is still returned")
			}
		}
	}
	if c.service != nil || c.batch.Running || c.importStatus.Running {
		t.Fatal("log query started resource work")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("log query wrote to the log", err)
	}
}

func TestLogsAPIAggregatesLiveSegments(t *testing.T) {
	c := testConsole(t)
	writer, err := applog.Open(c.files().LogDir, 256, 8192, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	logger := applog.New(writer, applog.Info)
	for i := 0; i < 20; i++ {
		logger.Info("segment_fixture", "index", i)
	}
	for _, path := range []string{"/api/logs?event=segment_fixture", "/api/logs/export?event=segment_fixture"} {
		r := httptest.NewRequest("GET", "http://"+c.address+path, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("cannot read live segments", w.Code, w.Body.String())
		}
		if strings.HasPrefix(path, "/api/logs/export") {
			if w.Header().Get("X-SWD-Log-Matches") != "20" || strings.Count(w.Body.String(), "[INFO] segment_fixture\n") != 20 {
				t.Fatal("export lost segment records", w.Body.String())
			}
			continue
		}
		var result applog.ReadResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Matches != 20 || len(result.Files) <= 1 {
			t.Fatal("query did not combine multiple live segments", result, err)
		}
		for _, file := range result.Files {
			if file == "app.log" {
				t.Fatal("writer used the removed anchor filename")
			}
		}
	}
	if c.service != nil {
		t.Fatal("log reads started the resource engine")
	}
}

func TestLogExportAPIUsesAuthenticatedFiltersAndAttachments(t *testing.T) {
	c := testConsole(t)
	path := filepath.Join(c.files().LogDir, "20261102T010000Z.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"time\":\"2026-11-02T02:00:00Z\",\"level\":\"WARN\",\"msg\":\"export_warning\",\"precise\":9007199254740993}\ninvalid\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query, filename, content string
		code, count              int
	}{
		{"?level=WARN&from=1793584800000&to=1793584801000&event=%20WARN%20", "swd-logs.txt", "[WARN] export_warning\nprecise=9007199254740993", 200, 1},
		{"?event=export_warn", "swd-logs.txt", "[WARN] export_warning\n", 200, 1},
		{"?event=missing", "swd-logs.txt", "", 200, 0},
		{"?level=ERROR", "swd-logs.txt", "", 200, 0},
		{"?level=invalid", "", "", 400, 0},
		{"?from=2&to=1", "", "", 400, 0},
	} {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/logs/export"+tc.query, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("export %s: %d %s", tc.query, w.Code, w.Body.String())
		}
		if tc.code == 200 {
			if w.Header().Get("Content-Disposition") != `attachment; filename="`+tc.filename+`"` || w.Header().Get("X-SWD-Log-Matches") != fmt.Sprint(tc.count) || w.Header().Get("X-SWD-Log-Skipped") != "1" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("invalid attachment metadata", w.Header())
			}
			if tc.count == 0 && w.Body.Len() != 0 || !strings.Contains(w.Body.String(), tc.content) {
				t.Fatal("wrong exported content", w.Body.String())
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) || c.service != nil {
		t.Fatal("export modified source logs or started an engine", err)
	}
}

// Opt-in Edge integration uses real JSONHandler output and the actual Console
// router, without starting the video gateway or changing production settings.
func TestLogReaderBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_LOG_READER_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_LOG_READER_BROWSER_EVIDENCE for real Console log reader integration")
	}
	c := testConsole(t)
	path := filepath.Join(c.files().LogDir, "20261102T020000Z.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name  string
		hour  int
		level slog.Level
		msg   string
	}{{"20261102T010000Z.log", 1, slog.LevelWarn, "fixture_warning"}, {"20261102T020000Z.log", 2, slog.LevelInfo, "fixture_info"}, {"20261102T020000Z.log", 3, slog.LevelError, "fixture_error"}} {
		var output bytes.Buffer
		record := slog.NewRecord(time.Date(2026, 11, 2, file.hour, 0, 0, 0, time.UTC), file.level, file.msg, 0)
		record.Add("outcome", "fixture", "precise", int64(9007199254740993), "detail", "<script>literal text</script>\nnot_a_key=inside value", "song_ids", []int{1343, -1}, "path", "C:/logs/"+strings.Repeat("long-path/", 20), "text", "two  spaces")
		if err := applog.New(&output, applog.Debug).Handler().Handle(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(c.files().LogDir, file.name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write(output.Bytes())
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal(writeErr, closeErr)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c.settings.LogLevel = "off"
	c.savedSettings = c.settings
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "log-reader-live.mjs"), ui.URL, evidence)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("log reader browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || c.service != nil {
		t.Fatal("log reader changed source evidence or started an engine", err)
	}
}
