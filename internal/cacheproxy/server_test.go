package cacheproxy

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const payload = "0123456789abcdefghijklmnopqrstuvwxyz"

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func videoURL(body string) string {
	return fmt.Sprintf("http://play.udon.dance/files/2403/1344-660524b4ebadb.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body))
}

func setup(t *testing.T, handler http.HandlerFunc) (*Server, Config) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	cfg := DefaultConfig()
	cfg.OriginScheme = "http"
	cfg.StorageDir = t.TempDir()
	cfg.ResolveCurrent = func(context.Context, string) (string, error) { return "", errors.New("test API unavailable") }
	cfg.Logger = slog.New(slog.NewTextHandler(testLogWriter{t}, nil))
	for h := range cfg.Origins {
		cfg.Origins[h] = strings.TrimPrefix(upstream.URL, "http://")
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, cfg
}

type responseRecorder struct {
	*httptest.ResponseRecorder
	aborted bool
}

func request(s *Server, method, target string, headers map[string]string) (w *responseRecorder) {
	w = &responseRecorder{ResponseRecorder: httptest.NewRecorder()}
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered != http.ErrAbortHandler {
				panic(recovered)
			}
			w.aborted = true // Emulate net/http's connection-abort boundary, preserving actual status/body.
		}
	}()
	r := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	s.ServeHTTP(w, r)
	return w
}

func assertResponse(t *testing.T, w *responseRecorder, status int, body string) {
	t.Helper()
	if w.aborted || w.Code != status || w.Body.String() != body {
		t.Fatalf("got %d %q, want %d %q", w.Code, w.Body.String(), status, body)
	}
}

func assertNoPartial(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.part"))
	if err != nil || len(files) != 0 {
		t.Fatalf("partials %v, err %v", files, err)
	}
}

func TestColdHotCrossHostAndHTTP(t *testing.T) {
	var count atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Host != "play.udon.dance" || r.Method != "GET" || r.Header.Get("Range") != "" || r.URL.RawQuery == "" {
			t.Errorf("unexpected origin request: %v", r)
		}
		io.WriteString(w, payload)
	})
	w := request(s, "GET", videoURL(payload), nil)
	assertResponse(t, w, 200, payload)
	if w.Header().Get("X-StepStash-Cache") != "MISS" {
		t.Fatal(w.Header())
	}
	lastModified := request(s, "HEAD", videoURL(payload), nil).Header().Get("Last-Modified")
	etag := w.Header().Get("ETag")
	for _, tc := range []struct {
		name, method, rangeValue string
		status                   int
		body, cr                 string
	}{
		{"full", "GET", "", 200, payload, ""},
		{"head", "HEAD", "", 200, "", ""},
		{"head ignores range", "HEAD", "bytes=2-4", 200, "", ""},
		{"start", "GET", "bytes=0-3", 206, "0123", "bytes 0-3/36"},
		{"middle", "GET", "bytes=10-13", 206, "abcd", "bytes 10-13/36"},
		{"suffix", "GET", "bytes=-3", 206, "xyz", "bytes 33-35/36"},
		{"open", "GET", "bytes=33-", 206, "xyz", "bytes 33-35/36"},
		{"clamped", "GET", "bytes=33-100", 206, "xyz", "bytes 33-35/36"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1)
			w := request(s, tc.method, u, map[string]string{"Range": tc.rangeValue, "Cache-Control": "no-cache"})
			assertResponse(t, w, tc.status, tc.body)
			if w.Header().Get("Content-Range") != tc.cr || w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("Content-Type") != "video/mp4" || w.Header().Get("X-StepStash-Cache") != "HIT" {
				t.Fatal(w.Header())
			}
			if tc.method == "HEAD" && w.Header().Get("Content-Length") != "36" {
				t.Fatal(w.Header())
			}
		})
	}
	for _, rng := range []string{"bytes=100-", "bytes=abc", "bytes=3-2", "bytes=0-1,3-4"} {
		w := request(s, "GET", videoURL(payload), map[string]string{"Range": rng})
		if w.Code != 416 {
			t.Fatalf("range %s: %d", rng, w.Code)
		}
	}
	for _, tc := range []struct {
		headers map[string]string
		status  int
		body    string
	}{
		{map[string]string{"If-None-Match": etag}, 304, ""},
		{map[string]string{"If-Match": `"wrong"`}, 412, ""},
		{map[string]string{"If-Match": etag, "Range": "bytes=0-3"}, 206, "0123"},
		{map[string]string{"If-Modified-Since": lastModified}, 304, ""},
		{map[string]string{"If-Unmodified-Since": "Mon, 02 Jan 2006 15:04:05 GMT"}, 412, ""},
		{map[string]string{"If-Range": `"wrong"`, "Range": "bytes=0-3"}, 200, payload},
	} {
		assertResponse(t, request(s, "GET", videoURL(payload), tc.headers), tc.status, tc.body)
	}
	if count.Load() != 1 {
		t.Fatalf("origin calls: %d", count.Load())
	}
}

func TestRejectedRequestsNeverReachOrigin(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected origin access") })
	for _, target := range []string{
		strings.Replace(videoURL(payload), "play.udon.dance", "evil.example", 1),
		"http://play.udon.dance/Api/Songs/play?id=1344",
		"http://play.udon.dance/files/../video.mp4",
		strings.Split(videoURL(payload), "?")[0],
		videoURL(payload) + "&e=bad",
		videoURL(payload) + "&s=0",
		videoURL(payload) + "&bad=%zz",
		strings.Replace(videoURL(payload), "s=36", "s=-1", 1),
		strings.Replace(videoURL(payload), "s=36", "s=99999999999999999999999", 1),
		strings.Replace(videoURL(payload), "/1344-", "/%31%33%34%34-", 1),
	} {
		w := request(s, "GET", target, nil)
		if w.Code != 400 {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	w := request(s, "POST", videoURL(payload), nil)
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal(w)
	}
}

func TestBadUpstreamNeverPublishesAndRetries(t *testing.T) {
	for _, mode := range []string{"status", "redirect", "short", "long", "checksum", "encoding", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			var count atomic.Int32
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if count.Add(1) > 1 {
					io.WriteString(w, payload)
					return
				}
				switch mode {
				case "status":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "http://127.0.0.1:1/private")
					w.WriteHeader(302)
				case "short":
					io.WriteString(w, "short")
				case "long":
					w.(http.Flusher).Flush()
					io.WriteString(w, payload+"extra")
				case "checksum":
					io.WriteString(w, strings.Repeat("x", len(payload)))
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
					io.WriteString(w, payload)
				case "truncated":
					w.Header().Set("Content-Length", "36")
					io.WriteString(w, "short")
				}
			})
			w := request(s, "GET", videoURL(payload), nil)
			if w.Code != 502 && !(w.aborted && w.Body.Len() < len(payload)) {
				t.Fatalf("got %d", w.Code)
			}
			files, _ := filepath.Glob(filepath.Join(cfg.videosDir(), "*.mp4"))
			if len(files) != 0 {
				t.Fatal(files)
			}
			s.wg.Wait()
			assertNoPartial(t, cfg.tempDir())
			assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
			if count.Load() != 2 {
				t.Fatal(count.Load())
			}
		})
	}
}

func TestSharedDownloadSurvivesClientCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			io.WriteString(w, payload)
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", videoURL(payload), nil).WithContext(ctx)
	firstDone := make(chan struct{})
	go func() { s.ServeHTTP(httptest.NewRecorder(), r); close(firstDone) }()
	<-started
	cancel()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("disconnected caller stayed blocked")
	}
	const n = 12
	var wg sync.WaitGroup
	responses := make(chan *responseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=10-13"})
		}()
	}
	close(release)
	wg.Wait()
	close(responses)
	for w := range responses {
		assertResponse(t, w, 206, "abcd")
	}
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
	s.wg.Wait()
	assertNoPartial(t, cfg.tempDir())
}

func TestVersionIsolationCorruptionAndRestart(t *testing.T) {
	var count atomic.Int32
	var next atomic.Value
	next.Store(payload)
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(payload))) {
			io.WriteString(w, payload)
		} else {
			io.WriteString(w, next.Load().(string))
		}
	})
	cfg.ResolveCurrent = func(context.Context, string) (string, error) { return videoURL(next.Load().(string)), nil }
	s.cfg.ResolveCurrent = cfg.ResolveCurrent
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	files, _ := filepath.Glob(filepath.Join(cfg.videosDir(), "*.mp4"))
	if err := os.WriteFile(files[0], []byte(strings.Repeat("x", len(payload))), 0600); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	newPayload := "a new version"
	next.Store(newPayload)
	assertResponse(t, request(s, "GET", videoURL(newPayload), nil), 200, newPayload)
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.tempDir(), "download-crashed.part"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	assertNoPartial(t, cfg.tempDir())
	assertResponse(t, request(restarted, "GET", videoURL(payload), nil), 200, payload)
	assertResponse(t, request(restarted, "GET", videoURL(newPayload), nil), 200, newPayload)
	// Raw URLs do not identify songs, so both resources remain cached.
	if count.Load() != 3 {
		t.Fatal(count.Load())
	}
}

func TestCanonicalStoreValidatesHits(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			var count atomic.Int32
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); io.WriteString(w, payload) })
			s.Close()
			dir := cfg.videosDir()
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			body := payload
			if !valid {
				body = "corrupt"
			}
			path := testVideoFile(t, cfg, payload)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			w := request(s, "GET", videoURL(payload), nil)
			assertResponse(t, w, 200, payload)
			if valid && (w.Header().Get("X-StepStash-Cache") != "HIT" || count.Load() != 0) {
				t.Fatal(w.Header(), count.Load())
			}
			if !valid && count.Load() != 1 {
				t.Fatal(count.Load())
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != payload {
				t.Fatal("source changed", err)
			}
		})
	}
}

func TestOwnershipCapacityTimeoutAndShutdown(t *testing.T) {
	started := make(chan struct{}, 2)
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "36")
		io.WriteString(w, "0123")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	})
	if second, err := New(cfg); err == nil {
		second.Close()
		t.Fatal("second cache owner accepted")
	}
	s.Close()
	cfg.MaxDownloads = 1
	cfg.DownloadTimeout = 200 * time.Millisecond
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan *responseRecorder, 1)
	go func() { done <- request(s, "GET", videoURL(payload), nil) }()
	<-started
	w := request(s, "GET", videoURL("different"), nil)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = <-done
	if w.Code != 504 && !w.aborted {
		t.Fatal(w.Code)
	}
	s.wg.Wait()
	assertNoPartial(t, cfg.tempDir())
	go func() { done <- request(s, "GET", videoURL(payload), nil) }()
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	w = <-done
	if w.Code != 503 && !w.aborted {
		t.Fatal(w.Code)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestHKGColdAndRealHTTP(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "nya.xin.moe" {
			t.Error(r.Host)
		}
		io.WriteString(w, payload)
	})
	local := httptest.NewServer(s)
	defer local.Close()
	r, _ := http.NewRequest("GET", local.URL+strings.TrimPrefix(videoURL(payload), "http://play.udon.dance"), nil)
	r.Host = "nya.xin.moe"
	r.Header.Set("Range", "bytes=10-13")
	resp, err := local.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 206 || string(body) != "abcd" {
		t.Fatal(resp.StatusCode, string(body), err)
	}
}
