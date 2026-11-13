package applog

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
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

func TestLoggerLocalIdentityAndHTTPBridge(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, Info)
	logger.With("component", "cache").Info("event", "at", time.Date(2026, 10, 27, 12, 0, 0, 0, time.FixedZone("east", 8*3600)))
	slog.NewLogLogger(logger.Handler(), slog.LevelError).Print("HTTP failure")
	var session string
	for i, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		text := string(line)
		identity := regexp.MustCompile(`(?:^| )session_id=([a-zA-Z0-9]+)(?: |$)`).FindStringSubmatch(text)
		if !strings.Contains(text, " pid="+strconv.Itoa(os.Getpid())+" ") || len(identity) != 2 {
			t.Fatal(text)
		}
		if i == 0 {
			session = identity[1]
			want := time.Date(2026, 10, 27, 12, 0, 0, 0, time.FixedZone("east", 8*3600)).Local().Format(timeLayout)
			if !strings.Contains(text, "at="+want) || !strings.Contains(text, "component=cache") {
				t.Fatal(text)
			}
		} else if identity[1] != session || !strings.Contains(text, `[ERROR] "HTTP failure"`) {
			t.Fatal(text)
		}
	}
	output.Reset()
	New(&output, Info).Info("new_session")
	if strings.Contains(output.String(), session) {
		t.Fatal("different application sessions share identity")
	}
}

func TestTextRecordsPreserveContentAndTime(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("test", 8*3600)
	t.Cleanup(func() { time.Local = previous })
	at := time.Date(2026, 11, 13, 12, 0, 0, 123456789, time.UTC)
	for _, value := range []string{"before�after", "before\xffafter", "first\nsecond \"line\"", "\x1b[31mred\x1b[0m", "time=x level=ERROR msg=fake"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			var output bytes.Buffer
			h := New(&output, Info).With("component", "cache").WithGroup("task").Handler()
			r := slog.NewRecord(at, slog.LevelInfo, "消息包含 msg=fake\n下一行", 0)
			r.Add("value", value, "at", at, "unset", time.Time{}, "precise", int64(9007199254740993))
			if err := h.Handle(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			for _, want := range []string{"2026-11-13T20:00:00.1234+08:00 [INFO] ", "task.value=" + strconv.Quote(value), "task.at=2026-11-13T20:00:00.1234+08:00", "task.unset=0001-01-01T00:00:00.0000Z", "task.precise=9007199254740993", `msg=fake\n下一行`} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q: %q", want, text)
				}
			}
			if strings.Count(text, "\n") != 1 || strings.ContainsRune(text, '\x1b') {
				t.Fatalf("record contains unescaped controls: %q", text)
			}
		})
	}
}

type shortLogWriter struct{ err error }

func (w shortLogWriter) Write(p []byte) (int, error) { return len(p) - 1, w.err }

func TestTextWriterReportsInputBytesAndErrors(t *testing.T) {
	input := []byte("time=2026-11-13T20:00:00.0000+08:00 level=INFO msg=sample value=1\n")
	var output bytes.Buffer
	if n, err := (textWriter{&output}).Write(input); err != nil || n != len(input) {
		t.Fatal(n, err)
	}
	if output.String() != "2026-11-13T20:00:00.0000+08:00 [INFO] sample value=1\n" {
		t.Fatal(output.String())
	}
	failure := errors.New("write failure")
	for _, tc := range []struct{ actual, want error }{{nil, io.ErrShortWrite}, {failure, failure}} {
		if _, err := (textWriter{shortLogWriter{tc.actual}}).Write(input); !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	output.Reset()
	if _, err := (textWriter{&output}).Write([]byte("level=WARN msg=untimed\n")); err != nil || output.String() != "[WARN] untimed\n" {
		t.Fatal(output.String(), err)
	}
}
