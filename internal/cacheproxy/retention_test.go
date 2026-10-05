package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func putRetained(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("v", size)), 0600); err != nil {
		t.Fatal(err)
	}
}
func expectRetained(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if !want {
		deadline := time.Now().Add(3 * time.Second)
		for err == nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			_, err = os.Stat(path)
		}
	}
	if want && err != nil || !want && !os.IsNotExist(err) {
		t.Fatalf("%s existence want %v: %v", path, want, err)
	}
}
func TestRetentionScore(t *testing.T) {
	now := time.Now().UnixMilli()
	week := (7 * 24 * time.Hour).Milliseconds()
	if retentionScore(0, now, now) != 0 || retentionScore(3, now, now) <= retentionScore(1, now, now) || retentionScore(100, now-100*week, now) >= retentionScore(1, now, now) {
		t.Fatal("frequency/aging order")
	}
}
func TestRetentionPriorityAndVersions(t *testing.T) {
	s, cfg := setup(t, nil)
	now := time.Now().UnixMilli()
	for _, id := range []string{"4", "1", "2"} {
		seedPrioritySong(t, s, id, strings.Repeat(id, 32))
	}
	for _, e := range []usageEvent{{id: "1", at: now, summaryOnly: true}, {id: "2", at: now - (100 * 24 * time.Hour).Milliseconds(), summaryOnly: true}} {
		if err := s.usage.write([]usageEvent{e}); err != nil {
			t.Fatal(err)
		}
	}
	hot := retainedPath(t, s, cfg, "1")
	cold := retainedPath(t, s, cfg, "2")
	prefetch := retainedPath(t, s, cfg, "3")
	metadata := filepath.Join(cfg.StorageDir, "notes.json")
	copyPath := cfg.videoFile(strings.Repeat("4", 32))
	s.usage.write([]usageEvent{{id: "4", at: now - 1, summaryOnly: true}})
	for _, p := range []string{hot, cold, prefetch, copyPath, metadata} {
		putRetained(t, p, 10)
	}
	setRetentionLimit(t, s, 20)
	s.trimCache()
	expectRetained(t, hot, true)
	expectRetained(t, copyPath, true)
	expectRetained(t, cold, false)
	expectRetained(t, prefetch, false)
	expectRetained(t, metadata, true)
	setRetentionLimit(t, s, 10)
	s.trimCache()
	expectRetained(t, hot, true)
	expectRetained(t, copyPath, false)
}
func TestRetentionPinsAndUnlimited(t *testing.T) {
	s, cfg := setup(t, nil)
	p := retainedPath(t, s, cfg, "1")
	putRetained(t, p, 10)
	s.trimCache()
	expectRetained(t, p, true)
	setRetentionLimit(t, s, 1)
	v := video{key: strings.Repeat("1", 32)}
	s.pinVideo(v)
	s.pinVideo(v)
	s.trimCache()
	expectRetained(t, p, true)
	s.releaseVideo(v)
	expectRetained(t, p, true)
	s.releaseVideo(v)
	expectRetained(t, p, false)
}
func TestRetentionStartupAndUnknownFiles(t *testing.T) {
	cfg := fixtureConfig()
	cfg.OriginScheme = "http"
	cfg.StorageDir = t.TempDir()
	cfg.MaxCacheBytes = 1
	p := cfg.videoFile(strings.Repeat("a", 32))
	unknown := filepath.Join(cfg.videosDir(), "personal.mp4")
	putRetained(t, p, 10)
	putRetained(t, unknown, 10)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expectRetained(t, p, false)
	expectRetained(t, unknown, true)
}
func TestOversizedVideoStillServed(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	setRetentionLimit(t, s, 1)
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	// Close waits for the independent download worker and its eviction pass.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	expectRetained(t, testVideoFile(t, cfg, payload), false)
	files, err := filepath.Glob(filepath.Join(cfg.videosDir(), "*.mp4"))
	if err != nil || len(files) != 0 {
		t.Fatalf("retained %v: %v", files, err)
	}
}
func TestPrefetchDoesNotDisplacePopularVideo(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	p := retainedPath(t, s, cfg, "1")
	putRetained(t, p, len(payload))
	seedPrioritySong(t, s, "1", strings.Repeat("1", 32))
	s.usage.startGET("1", "")
	setRetentionLimit(t, s, int64(len(payload)))
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	expectRetained(t, p, true)
	expectRetained(t, testVideoFile(t, cfg, payload), false)
}

func TestProtectedLowPriorityDoesNotEvictHotVideo(t *testing.T) {
	s, cfg := setup(t, nil)
	hot := retainedPath(t, s, cfg, "1")
	low := retainedPath(t, s, cfg, "2")
	putRetained(t, hot, 10)
	putRetained(t, low, 10)
	seedPrioritySong(t, s, "1", strings.Repeat("1", 32))
	s.usage.startGET("1", "")
	setRetentionLimit(t, s, 10)
	v := video{key: strings.Repeat("2", 32)}
	s.pinVideo(v)
	s.trimCache()
	expectRetained(t, hot, true)
	expectRetained(t, low, true)
	s.releaseVideo(v)
	expectRetained(t, hot, true)
	expectRetained(t, low, false)
}

func TestRetentionProtectsActiveHTTPResponse(t *testing.T) {
	s, cfg := setup(t, nil)
	p := testVideoFile(t, cfg, payload)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	setRetentionLimit(t, s, 1)
	w := &pausedUsageWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(w, httptest.NewRequest("GET", videoURL(payload), nil)) }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		close(w.release)
		t.Fatal("response never started")
	}
	s.trimCache()
	_, statErr := os.Stat(p)
	close(w.release)
	<-done
	if statErr != nil {
		t.Fatal("active response evicted", statErr)
	}
	if w.Code != 200 || w.Body.String() != payload {
		t.Fatal("response corrupted")
	}
	expectRetained(t, p, false)
}

func TestReleaseOnlyCleansAfterLastReference(t *testing.T) {
	s, cfg := setup(t, nil)
	hot := retainedPath(t, s, cfg, "1")
	cold := retainedPath(t, s, cfg, "2")
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	seedPrioritySong(t, s, "1", strings.Repeat("1", 32))
	s.usage.startGET("1", "")
	setRetentionLimit(t, s, 10)
	v := video{key: strings.Repeat("a", 32)}
	s.pinVideo(v)
	s.pinVideo(v)
	s.releaseVideo(v)
	// Even an unprotected eviction candidate stays until the final release.
	expectRetained(t, cold, true)
	s.releaseVideo(v)
	expectRetained(t, cold, false)
	expectRetained(t, hot, true)
}

func TestRetentionWithinLimitSkipsUsageDatabase(t *testing.T) {
	s, cfg := setup(t, nil)
	p := retainedPath(t, s, cfg, "1")
	putRetained(t, p, 10)
	// A closed store makes any unnecessary statistics query fail deterministically.
	s.usage.close()
	for _, limit := range []int64{11, 10} {
		setRetentionLimit(t, s, limit)
		if err := s.trimCachePass(true); err != nil {
			t.Fatalf("within limit %d accessed statistics: %v", limit, err)
		}
		expectRetained(t, p, true)
	}
	setRetentionLimit(t, s, 9)
	if err := s.trimCachePass(true); err == nil {
		t.Fatal("over-limit cleanup should need statistics")
	}
	expectRetained(t, p, true)
}

func TestRetentionUsesResourceFingerprint(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("1", 32)
	seedPrioritySong(t, s, "1", key)
	hot := cfg.videoFile(key)
	cold := retainedPath(t, s, cfg, "2")
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	now := time.Now().UnixMilli()
	if err := s.usage.write([]usageEvent{
		{id: "1", at: now, summaryOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	setRetentionLimit(t, s, 20)
	s.trimCache()
	setRetentionLimit(t, s, 10)
	s.trimCache()
	// The filename identifies the resource without inferring a song.
	expectRetained(t, hot, true)
	expectRetained(t, cold, false)
}

// Production settings are immutable; tests change the limit under the same lock
// used by the worker and explicitly index their externally seeded files.
func setRetentionLimit(t *testing.T, s *Server, limit int64) {
	t.Helper()
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.retentionMu.Lock()
	s.cfg.MaxCacheBytes = limit
	s.retentionMu.Unlock()
	if err := s.reconcileRetention(); err != nil {
		t.Fatal(err)
	}
}

func retainedBytes(s *Server) int64 {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	return s.retainedBytes
}

func TestRetentionHitDoesNotRescanDirectory(t *testing.T) {
	s, cfg := setup(t, nil)
	path := testVideoFile(t, cfg, payload)
	writeTestFile(t, path, payload)
	setRetentionLimit(t, s, 1<<20)
	external := cfg.videoFile(strings.Repeat("a", 32))
	putRetained(t, external, 10)
	for i := 0; i < 3; i++ {
		assertResponse(t, request(s, "HEAD", videoURL(payload), nil), 200, "")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := retainedBytes(s); got != int64(len(payload)) {
		t.Fatalf("ordinary hit rescanned the directory: tracked %d bytes", got)
	}
}

func TestRetentionReleaseDoesNotWaitForWorker(t *testing.T) {
	s, cfg := setup(t, nil)
	path := testVideoFile(t, cfg, payload)
	writeTestFile(t, path, payload)
	setRetentionLimit(t, s, 1)
	s.retentionRunMu.Lock()
	done := make(chan *responseRecorder, 1)
	go func() { done <- request(s, "HEAD", videoURL(payload), nil) }()
	select {
	case w := <-done:
		assertResponse(t, w, 200, "")
	case <-time.After(3 * time.Second):
		s.retentionRunMu.Unlock()
		t.Fatal("HTTP release waited for the eviction worker")
	}
	expectRetained(t, path, true)
	s.retentionRunMu.Unlock()
	expectRetained(t, path, false)
}

func TestRetentionReconciliationCorrectsExternalChanges(t *testing.T) {
	s, cfg := setup(t, nil)
	setRetentionLimit(t, s, 100)
	one := cfg.videoFile(strings.Repeat("1", 32))
	two := cfg.videoFile(strings.Repeat("2", 32))
	putRetained(t, one, 10)
	putRetained(t, two, 20)
	s.runRetention(true) // The same pass used by the periodic timer.
	if got := retainedBytes(s); got != 30 {
		t.Fatalf("external additions: %d", got)
	}
	if err := os.Remove(one); err != nil {
		t.Fatal(err)
	}
	putRetained(t, two, 15)
	s.runRetention(true)
	if got := retainedBytes(s); got != 15 {
		t.Fatalf("external deletion/resize: %d", got)
	}
}

func TestRetentionPublicationAndEvictionAccounting(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	setRetentionLimit(t, s, 100)
	for i := 0; i < 2; i++ {
		if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if got := retainedBytes(s); got != int64(len(payload)) {
		t.Fatalf("publication/hit accounting: %d", got)
	}
	// No reconciliation: eviction must use the publication's tracked bytes.
	s.retentionMu.Lock()
	s.cfg.MaxCacheBytes = 1
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	expectRetained(t, testVideoFile(t, cfg, payload), false)
	if got := retainedBytes(s); got != 0 {
		t.Fatalf("eviction accounting: %d", got)
	}
}

func TestRetentionReconciliationDuringPublication(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	setRetentionLimit(t, s, 1<<20)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				s.runRetention(true)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for i := 1; i <= 12; i++ {
		target := strings.Replace(videoURL(payload), "1344-", fmt.Sprint(i)+"-", 1)
		if _, err := s.Prefetch(context.Background(), target); err != nil {
			t.Fatal(err)
		}
		// Serialize with the current scan without starting a new reconciliation
		// that could conceal a publication lost by the previous scan.
		s.retentionRunMu.Lock()
		got := retainedBytes(s)
		s.retentionRunMu.Unlock()
		if want := int64(len(payload)); got != want {
			t.Fatalf("concurrent publication accounting: %d, want %d", got, want)
		}
	}
}
