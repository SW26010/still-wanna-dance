package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
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
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/testlog"
)

func TestPrefetchResolutionLogsKeepOperationIdentity(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("fixed=%v/failed=%v", fixed, failed), func(t *testing.T) {
				logs := new(logCapture)
				s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) }, func(cfg *Config) {
					cfg.CacheUnlimited = true
					cfg.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
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
					if record["event_name"] == "prefetch_resolved" {
						resolved++
						if record["trace_id"] != applog.TraceID(ctx) || fmt.Sprint(record["song_id"]) != "42" || record["elapsed_ms"] == nil || record["success"] != !failed {
							t.Fatal("resolution lost operation details", record)
						}
						if !failed && record["resource_key"] != key {
							t.Fatal("resolution lost resource", record)
						}
					}
					if record["event_name"] == "playback_check_failed" && (record["trace_id"] != applog.TraceID(ctx) || fmt.Sprint(record["song_id"]) != "42") {
						t.Fatal("failure lost trace or lookup song", record)
					}
					if record["event_name"] == "prefetch_finished" {
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
		r, err := testlog.Parse(line)
		if err != nil {
			t.Fatal(err, string(line))
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
	cfg.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
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

func TestDownloadLogLevels(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		for _, level := range []applog.Level{applog.Info, applog.Debug} {
			t.Run(fmt.Sprintf("unlimited=%v/level=%s", unlimited, level), func(t *testing.T) {
				bodies := []string{payload, "abcdefghijklmnopqrstuvwxyz0123456789"}
				logs := new(logCapture)
				s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
					var body string
					for _, candidate := range bodies {
						if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(candidate))) {
							body = candidate
						}
					}
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || body == "" || start < 0 || end >= int64(len(body)) {
						t.Errorf("invalid test request range")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.WriteHeader(http.StatusPartialContent)
					io.WriteString(w, body[start:end+1])
				}, func(cfg *Config) {
					cfg.Logger = applog.New(logs, level)
					cfg.CacheUnlimited = unlimited
					cfg.VideoBudgetBytes = 0
					cfg.BlockSize = 8
				})
				for _, body := range bodies {
					assertResponse(t, request(s, "GET", videoURL(body), nil), http.StatusOK, body)
					s.wg.Wait() // The response can finish before the shared task.
				}
				s.Close()
				counts := map[string]int{}
				records := logs.records(t)
				t.Logf("two multi-block downloads: %d records", len(records))
				for _, record := range records {
					message, severity := fmt.Sprint(record["event_name"]), fmt.Sprint(record["level"])
					counts[message]++
					if severity == "WARN" || severity == "ERROR" {
						t.Errorf("unexpected %s for normal downloads: %s", severity, message)
					}
					if message == "cache_engine_ready" && (record["video_budget_bytes"] != float64(0) || record["cache_unlimited"] != unlimited) {
						t.Error("missing configured video budget", record)
					}
					if message == "request_finished" && (severity != "INFO" || record["outcome"] != "completed") {
						t.Error("missing successful request summary", record)
					}
					if message == "cache_task_finished" {
						want := "not_retained"
						if unlimited {
							want = "completed"
						}
						if severity != "INFO" || record["outcome"] != want {
							t.Error("missing shared task result", record)
						}
						wantMessage := "资源准备任务结束，未保留完整视频"
						if unlimited {
							wantMessage = "资源准备任务已完成"
						}
						if record["msg"] != wantMessage {
							t.Error("summary misrepresents retention result", record)
						}
					}
				}
				for _, event := range []string{"cache_engine_ready", "request_finished", "cache_task_finished"} {
					want := 2
					if event == "cache_engine_ready" {
						want = 1
					}
					if counts[event] != want {
						t.Errorf("%s: got %d, want %d", event, counts[event], want)
					}
				}
				if unlimited && counts["download_published"] != 2 || !unlimited && counts["download_published"] != 0 {
					t.Error("incorrect published video summaries", counts["download_published"])
				}
				for _, event := range []string{"cache_task_attached", "download_started", "download_stage", "download_first_byte", "upstream_response", "request_started", "served", "block_schedule"} {
					if level == applog.Info && counts[event] != 0 || level == applog.Debug && counts[event] == 0 {
						t.Errorf("%s: got %d at %s", event, counts[event], level)
					}
				}
				if !unlimited && (level == applog.Info && counts["cache_not_retained"] != 0 || level == applog.Debug && counts["cache_not_retained"] != 2) {
					t.Errorf("cache_not_retained: got %d at %s", counts["cache_not_retained"], level)
				}
			})
		}
	}
}

func TestLogsLinkSharedDownloadToBothRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		serveFixtureContent(w, r, payload)
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
			if r["event_name"] == "cache_task_attached" {
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
		switch r["event_name"] {
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
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) {
		serveFixtureContent(w, r, strings.Repeat("x", len(payload)))
	})
	request(s, "GET", videoURL(payload), nil)
	s.wg.Wait()
	s.Close()
	failed, terminal := false, false
	for _, r := range logs.records(t) {
		switch r["event_name"] {
		case "download_published":
			t.Fatal("published corrupt data")
		case "download_integrity_failed":
			failed = true
			if r["level"] != "WARN" {
				t.Fatal("integrity failure is not a warning", r)
			}
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
		serveFixtureContent(w, r, payload)
	})
	if _, err := s.Prefetch(t.Context(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	found := 0
	for _, record := range logs.records(t) {
		if record["event_name"] == "upstream_response" {
			found++
			if record["host"] != "play.udon.dance" || record["upstream_host"] != "nya.xin.moe" {
				t.Fatal("redirect lost original or actual upstream host", record)
			}
		}
	}
	if found != 1 {
		t.Fatal("missing upstream response record", found)
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
		if r["event_name"] == "cache_task_failed" {
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
	s, logs := loggingServer(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.ServeHTTP(brokenWriter{make(http.Header)}, httptest.NewRequest("GET", videoURL(payload), nil))
	s.Close()
	for _, r := range logs.records(t) {
		if r["event_name"] == "request_finished" {
			if r["status"] != float64(200) || r["outcome"] != "failed" || r["write_error"] == nil {
				t.Fatal(r)
			}
			return
		}
	}
	t.Fatal("missing request result")
}

func TestBytesPerSecondRequiresElapsedTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bytes   int64
		elapsed time.Duration
		want    float64
	}{
		{"stalled with equal timestamps", 0, 0, 0},
		{"bytes with equal timestamps", 2048, 0, 0},
		{"clock before previous sample", 2048, -time.Second, 0},
		{"one second", 2048, time.Second, 2048},
		{"fractional second", 512, 500 * time.Millisecond, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bytesPerSecond(tc.bytes, tc.elapsed); got != tc.want {
				t.Fatalf("bytesPerSecond(%d, %s) = %v; want %v", tc.bytes, tc.elapsed, got, tc.want)
			}
		})
	}
}

func TestProgressRateSampleKeepsBytesUntilTimeAdvances(t *testing.T) {
	start := time.Unix(100, 0)
	sample := progressRateSample{at: start}
	if got := sample.rate(start, 2048); got != 0 {
		t.Fatal("same-time sample produced a rate", got)
	}
	if got := sample.rate(start.Add(-time.Second), 3072); got != 0 {
		t.Fatal("backward sample produced a rate", got)
	}
	if got := sample.rate(start.Add(time.Second), 3072); got != 3072 {
		t.Fatal("bytes were lost before the first measurable interval", got)
	}
	if got := sample.rate(start.Add(time.Second), 4096); got != 0 {
		t.Fatal("same-time sample produced a rate", got)
	}
	if got := sample.rate(start.Add(2*time.Second), 4096); got != 1024 {
		t.Fatal("bytes were lost between measurable intervals", got)
	}
}

func TestProgressReportsStallAndStopsBeforeTerminal(t *testing.T) {
	for _, level := range []applog.Level{applog.Info, applog.Debug} {
		t.Run(level.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := new(logCapture)
				p := startProgress(applog.New(logs, level), 10, time.Millisecond)
				finished := false
				defer func() {
					if !finished {
						p.finish(context.Canceled)
					}
				}()
				p.setStage("download")
				synctest.Wait()
				time.Sleep(time.Millisecond)
				synctest.Wait()
				found := false
				for _, r := range logs.records(t) {
					if r["event_name"] == "download_progress" && r["stage"] == "download" {
						found = true
						if r["bytes"] != float64(0) || r["bytes_per_second"] != float64(0) {
							t.Fatal(r)
						}
					}
				}
				if found != (level == applog.Debug) {
					t.Fatal("unexpected stalled progress at level", level)
				}
				p.finish(context.DeadlineExceeded)
				finished = true
				records := logs.records(t)
				last := records[len(records)-1]
				if last["event_name"] != "cache_task_finished" || last["outcome"] != "timeout" {
					t.Fatal(last)
				}
				time.Sleep(10 * time.Millisecond)
				synctest.Wait()
				if after := logs.records(t); len(after) != len(records) {
					t.Fatal("progress was reported after the terminal event", after[len(records):])
				}
			})
		})
	}
}
