package cacheproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/applog"
)

func TestPrefetchResolutionLogsKeepOperationIdentity(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("fixed=%v/failed=%v", fixed, failed), func(t *testing.T) {
				logs := new(logCapture)
				s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }, func(cfg *Config) {
					cfg.CacheUnlimited = true
					cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
					cfg.ResolvePlayback = func(context.Context, string) (string, error) {
						if failed {
							return "", errors.New("lookup unavailable")
						}
						return videoURL(payload) + "&token=secret", nil
					}
				})
				ctx := applog.WithTrace(t.Context())
				key := parsedVideo(t, s, payload).key
				if fixed {
					if err := syncTestCatalog(s, ctx, map[string]string{"42": key}); err != nil {
						t.Fatal(err)
					}
					ctx = withExpectedMD5(ctx, key)
				}
				_, err := s.PrefetchDownload(ctx, 42)
				if (err != nil) != failed {
					t.Fatal("unexpected download result", err)
				}
				s.wg.Wait()
				resolved, finished := 0, 0
				for _, record := range logs.records(t) {
					if record["msg"] == "prefetch_resolved" {
						resolved++
						if record["trace_id"] != applog.TraceID(ctx) || fmt.Sprint(record["song_id"]) != "42" || record["elapsed_ms"] == nil || record["success"] != !failed {
							t.Fatal("resolution lost operation details", record)
						}
						if !failed && record["resource_key"] != key {
							t.Fatal("resolution lost resource", record)
						}
					}
					if record["msg"] == "playback_check_failed" && (record["trace_id"] != applog.TraceID(ctx) || fmt.Sprint(record["song_id"]) != "42") {
						t.Fatal("failure lost trace or lookup song", record)
					}
					if record["msg"] == "prefetch_finished" {
						finished++
						if record["trace_id"] != applog.TraceID(ctx) {
							t.Fatal("download and resolution used different operations", record)
						}
					}
					encoded, _ := json.Marshal(record)
					if strings.Contains(string(encoded), "secret") {
						t.Fatal("resolution leaked URL", record)
					}
				}
				if resolved != 1 || !failed && finished != 1 {
					t.Fatal("missing resolution or completion event", resolved, finished)
				}
			})
		}
	}
}

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
		decoder := json.NewDecoder(bytes.NewReader(line))
		if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
			t.Fatal(err)
		}
		r := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				t.Fatal(err)
			}
			key := token.(string)
			if _, exists := r[key]; exists {
				t.Fatalf("duplicate log field %q: %s", key, line)
			}
			var value any
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			r[key] = value
		}
		if _, err := decoder.Token(); err != nil {
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
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
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
	s.wg.Wait() // Publication finishes independently of the GETs.
	s.Close()
	var flight any
	traces := map[any]bool{}
	finished, published, upstream := 0, 0, 0
	for _, r := range logs.records(t) {
		if r["service_id"] == nil {
			t.Fatal("missing service identity", r)
		}
		switch r["msg"] {
		case "upstream_response":
			upstream++
			if r["host"] == nil || r["upstream_host"] == nil {
				t.Fatal("missing resource or selected upstream host", r)
			}
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
	if len(traces) != 2 || finished != 2 || published != 1 || upstream == 0 {
		t.Fatal(traces, finished, published, upstream)
	}
}

func TestLogsSeparateGETDeliveryFromIntegrityFailure(t *testing.T) {
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", len(payload))) })
	request(s, "GET", videoURL(payload), nil)
	s.wg.Wait()
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
			if r["outcome"] == "completed" && r["bytes"] != float64(len(payload)) {
				t.Fatal(r)
			}
		}
	}
	if !failed || !terminal {
		t.Fatal("missing failure evidence")
	}
}

func TestLogsPreserveResourceHostAcrossRedirect(t *testing.T) {
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "play.udon.dance" {
			http.Redirect(w, r, strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1), http.StatusFound)
			return
		}
		io.WriteString(w, payload)
	})
	if _, err := s.Prefetch(t.Context(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	found := 0
	for _, record := range logs.records(t) {
		if record["msg"] == "upstream_response" || record["msg"] == "range_unavailable" {
			found++
			if record["host"] != "play.udon.dance" || record["upstream_host"] != "nya.xin.moe" {
				t.Fatal("redirect lost original or actual upstream host", record)
			}
		}
	}
	if found != 2 {
		t.Fatal("missing upstream or sequential fallback record", found)
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
	p.setStage("download")
	deadline := time.Now().Add(time.Second)
	for {
		found := false
		for _, r := range logs.records(t) {
			if r["msg"] == "download_progress" && r["stage"] == "download" {
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
