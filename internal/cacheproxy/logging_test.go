package cacheproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type logCapture struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (c *logCapture) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.Write(b)
}
func (c *logCapture) records(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	data := append([]byte(nil), c.data.Bytes()...)
	c.mu.Unlock()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}

func loggingServer(t *testing.T, handler http.HandlerFunc) (*Server, *logCapture) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	logs := new(logCapture)
	cfg := DefaultConfig()
	cfg.OriginScheme = "http"
	cfg.StorageDir = t.TempDir()
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(upstream.URL, "http://")
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, logs
}

func TestLogsLinkSharedDownloadToBothRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, payload)
	})
	defer unblock()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); request(s, "GET", videoURL(payload)+"&token=secret", nil) }()
		if i == 0 {
			<-entered
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		attached := 0
		for _, r := range logs.records(t) {
			if r["msg"] == "cache_task_attached" {
				attached++
			}
		}
		if attached == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("requests did not share flight")
		}
		time.Sleep(time.Millisecond)
	}
	unblock()
	wg.Wait()
	s.Close()
	var flight any
	traces := map[any]bool{}
	finished, published := 0, 0
	for _, r := range logs.records(t) {
		if r["service_id"] == nil {
			t.Fatal("missing service identity", r)
		}
		switch r["msg"] {
		case "cache_task_attached":
			if flight != nil && flight != r["flight_id"] {
				t.Fatal("different flight", r)
			}
			flight = r["flight_id"]
			traces[r["trace_id"]] = true
			if r["resource_key"] == nil {
				t.Fatal("missing resource", r)
			}
		case "request_finished":
			finished++
			if r["outcome"] != "completed" || r["bytes"] != float64(len(payload)) {
				t.Fatal(r)
			}
		case "download_published":
			published++
		}
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), "secret") {
			t.Fatal("query leaked")
		}
	}
	if len(traces) != 2 || finished != 2 || published != 1 {
		t.Fatal(traces, finished, published)
	}
}

func TestLogsIntegrityFailureDoesNotClaimCompletion(t *testing.T) {
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", len(payload))) })
	request(s, "GET", videoURL(payload), nil)
	s.Close()
	failed, terminal := false, false
	for _, r := range logs.records(t) {
		switch r["msg"] {
		case "download_published":
			t.Fatal("published corrupt data")
		case "download_integrity_failed":
			failed = true
		case "request_finished":
			terminal = true
			if r["outcome"] == "completed" {
				t.Fatal(r)
			}
		}
	}
	if !failed || !terminal {
		t.Fatal("missing failure evidence")
	}
}

func TestLogsMalformedLocationIsRedacted(t *testing.T) {
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://nya.xin.moe/%zz?token=secret")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err == nil {
		t.Fatal("expected redirect failure")
	}
	s.Close()
	failureLogged := false
	for _, r := range logs.records(t) {
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "token") {
			t.Fatalf("Location leaked: %s", encoded)
		}
		if r["msg"] == "cache_task_failed" {
			failureLogged = true
		}
	}
	if !failureLogged {
		t.Fatal("missing download failure")
	}
}

type brokenWriter struct{ header http.Header }

func (w brokenWriter) Header() http.Header       { return w.header }
func (w brokenWriter) WriteHeader(int)           {}
func (w brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLogsClientWriteFailureAfter200(t *testing.T) {
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.ServeHTTP(brokenWriter{make(http.Header)}, httptest.NewRequest("GET", videoURL(payload), nil))
	s.Close()
	for _, r := range logs.records(t) {
		if r["msg"] == "request_finished" {
			if r["status"] != float64(200) || r["outcome"] != "failed" || r["write_error"] == nil {
				t.Fatal(r)
			}
			return
		}
	}
	t.Fatal("missing request result")
}

func TestProgressReportsStallAndStopsBeforeTerminal(t *testing.T) {
	logs := new(logCapture)
	p := startProgress(slog.New(slog.NewJSONHandler(logs, nil)), 10, time.Millisecond)
	p.setStage("download_and_hash")
	deadline := time.Now().Add(time.Second)
	for {
		found := false
		for _, r := range logs.records(t) {
			if r["msg"] == "download_progress" && r["stage"] == "download_and_hash" {
				found = true
				if r["bytes"] != float64(0) || r["bytes_per_second"] != float64(0) {
					t.Fatal(r)
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no stalled progress")
		}
		time.Sleep(time.Millisecond)
	}
	p.finish(context.DeadlineExceeded)
	records := logs.records(t)
	last := records[len(records)-1]
	if last["msg"] != "cache_task_finished" || last["outcome"] != "timeout" {
		t.Fatal(last)
	}
}
