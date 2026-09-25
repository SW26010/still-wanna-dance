package cacheproxy

import (
	"context"
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
	for _, e := range []usageEvent{{id: strings.Repeat("1", 64), at: now, summaryOnly: true}, {id: strings.Repeat("2", 64), at: now - (100 * 24 * time.Hour).Milliseconds(), summaryOnly: true}} {
		if err := s.usage.write([]usageEvent{e}); err != nil {
			t.Fatal(err)
		}
	}
	hot := retainedPath(t, s, cfg, "1")
	cold := retainedPath(t, s, cfg, "2")
	prefetch := retainedPath(t, s, cfg, "3")
	metadata := filepath.Join(cfg.StorageDir, "notes.json")
	copyPath := cfg.videoFile(strings.Repeat("0", 64))
	s.usage.write([]usageEvent{{id: strings.Repeat("0", 64), at: now - 1, summaryOnly: true}})
	for _, p := range []string{hot, cold, prefetch, copyPath, metadata} {
		putRetained(t, p, 10)
	}
	s.cfg.MaxCacheBytes = 20
	s.trimCache()
	expectRetained(t, hot, true)
	expectRetained(t, copyPath, true)
	expectRetained(t, cold, false)
	expectRetained(t, prefetch, false)
	expectRetained(t, metadata, true)
	s.cfg.MaxCacheBytes = 10
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
	s.cfg.MaxCacheBytes = 1
	v := video{key: strings.Repeat("1", 64)}
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
	cfg := DefaultConfig()
	cfg.StorageDir = t.TempDir()
	cfg.MaxCacheBytes = 1
	p := cfg.videoFile(strings.Repeat("a", 64))
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
	s.cfg.MaxCacheBytes = 1
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
	s.usage.startDemand(strings.Repeat("1", 64))
	s.cfg.MaxCacheBytes = int64(len(payload))
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
	s.usage.startDemand(strings.Repeat("1", 64))
	s.cfg.MaxCacheBytes = 10
	v := video{key: strings.Repeat("2", 64)}
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
	s.cfg.MaxCacheBytes = 1
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
	s.usage.startDemand(strings.Repeat("1", 64))
	s.cfg.MaxCacheBytes = 10
	v := video{key: strings.Repeat("a", 64)}
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
		s.cfg.MaxCacheBytes = limit
		if err := s.trimCacheLocked(); err != nil {
			t.Fatalf("within limit %d accessed statistics: %v", limit, err)
		}
		expectRetained(t, p, true)
	}
	s.cfg.MaxCacheBytes = 9
	if err := s.trimCacheLocked(); err == nil {
		t.Fatal("over-limit cleanup should need statistics")
	}
	expectRetained(t, p, true)
}

func TestRetentionUsesResourceFingerprint(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("1", 64)
	hot := cfg.videoFile(key)
	cold := retainedPath(t, s, cfg, "2")
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	now := time.Now().UnixMilli()
	if err := s.usage.write([]usageEvent{
		{id: strings.Repeat("1", 64), at: now, summaryOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxCacheBytes = 20
	s.trimCache()
	s.cfg.MaxCacheBytes = 10
	s.trimCache()
	// The filename identifies the resource without inferring a song.
	expectRetained(t, hot, true)
	expectRetained(t, cold, false)
}
