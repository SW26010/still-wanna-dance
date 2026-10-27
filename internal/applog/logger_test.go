package applog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLevelsFilterRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{{"", 3}, {"info", 3}, {"debug", 4}, {"warn", 2}, {"error", 1}, {"off", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			level, err := ParseLevel(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			logger := New(&output, level).With("component", "test")
			for _, severity := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				logger.Log(t.Context(), severity, "test_event")
			}
			if got := bytes.Count(output.Bytes(), []byte("\n")); got != tc.want {
				t.Fatalf("got %d records, want %d: %s", got, tc.want, &output)
			}
			if tc.name == "off" && logger.Enabled(t.Context(), slog.Level(1000)) {
				t.Fatal("off must disable all records")
			}
		})
	}
	for _, value := range []string{"verbose", "INFO", " info", "false"} {
		if _, err := ParseLevel(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestLoggerUTCIdentityAndHTTPBridge(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, Info)
	logger.With("component", "cache").Info("event", "at", time.Date(2026, 10, 27, 12, 0, 0, 0, time.FixedZone("east", 8*3600)))
	slog.NewLogLogger(logger.Handler(), slog.LevelError).Print("HTTP failure")
	var session string
	for i, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["pid"] != float64(os.Getpid()) || record["session_id"] == "" || !strings.HasSuffix(record["time"].(string), "Z") {
			t.Fatal(record)
		}
		if i == 0 {
			session = record["session_id"].(string)
			if record["at"] != "2026-10-27T04:00:00Z" || record["component"] != "cache" {
				t.Fatal(record)
			}
		} else if record["session_id"] != session || record["level"] != "ERROR" || record["msg"] != "HTTP failure" {
			t.Fatal(record)
		}
	}
	output.Reset()
	New(&output, Info).Info("new_session")
	if strings.Contains(output.String(), session) {
		t.Fatal("different application sessions share identity")
	}
}
