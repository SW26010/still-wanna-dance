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
func TestRetentionPriorityAndCopies(t *testing.T) {
	s, cfg := setup(t, nil)
	now := time.Now().UnixMilli()
	for _, e := range []usageEvent{{id: "1", at: now, summaryOnly: true}, {id: "2", at: now - (100 * 24 * time.Hour).Milliseconds(), summaryOnly: true}} {
		if err := s.usage.write([]usageEvent{e}); err != nil {
			t.Fatal(err)
		}
	}
	hot := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	cold := filepath.Join(cfg.SongsDir, "2", "video.mp4")
	prefetch := filepath.Join(cfg.SongsDir, "3", "video.mp4")
	metadata := filepath.Join(cfg.SongsDir, "2", "metadata.json")
	copyPath := filepath.Join(cfg.CacheDir, strings.Repeat("a", 64)+".mp4")
	for _, p := range []string{hot, cold, prefetch, copyPath, metadata} {
		putRetained(t, p, 10)
	}
	s.versions[strings.Repeat("a", 64)] = "1"
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
	p := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	putRetained(t, p, 10)
	s.trimCache()
	expectRetained(t, p, true)
	s.cfg.MaxCacheBytes = 1
	v := video{id: "1", key: strings.Repeat("a", 64)}
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
	cfg.CacheDir = t.TempDir()
	cfg.SongsDir = t.TempDir()
	cfg.MaxCacheBytes = 1
	p := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	unknown := filepath.Join(cfg.CacheDir, "personal.mp4")
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
	expectRetained(t, filepath.Join(cfg.SongsDir, "1344", "video.mp4"), false)
	files, err := filepath.Glob(filepath.Join(cfg.CacheDir, "*.mp4"))
	if err != nil || len(files) != 0 {
		t.Fatalf("retained %v: %v", files, err)
	}
}
func TestPrefetchDoesNotDisplacePopularVideo(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	p := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	putRetained(t, p, len(payload))
	s.usage.startDemand("1")
	s.cfg.MaxCacheBytes = int64(len(payload))
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	expectRetained(t, p, true)
	expectRetained(t, filepath.Join(cfg.SongsDir, "1344", "video.mp4"), false)
}

func TestProtectedLowPriorityDoesNotEvictHotVideo(t *testing.T) {
	s, cfg := setup(t, nil)
	hot := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	low := filepath.Join(cfg.SongsDir, "2", "video.mp4")
	putRetained(t, hot, 10)
	putRetained(t, low, 10)
	s.usage.startDemand("1")
	s.cfg.MaxCacheBytes = 10
	v := video{id: "2", key: strings.Repeat("b", 64)}
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
	p := filepath.Join(cfg.SongsDir, "1344", "video.mp4")
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
	hot := filepath.Join(cfg.SongsDir, "1", "video.mp4")
	cold := filepath.Join(cfg.SongsDir, "2", "video.mp4")
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	s.usage.startDemand("1")
	s.cfg.MaxCacheBytes = 10
	v := video{id: "1", key: strings.Repeat("a", 64)}
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
	p := filepath.Join(cfg.SongsDir, "1", "video.mp4")
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

func TestRetentionLoadsHistoricalMappingOnlyWhenOverLimit(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("a", 64)
	hot := filepath.Join(cfg.CacheDir, key+".mp4")
	cold := filepath.Join(cfg.SongsDir, "2", "video.mp4")
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	now := time.Now().UnixMilli()
	if err := s.usage.write([]usageEvent{
		{id: "1", at: now, summaryOnly: true},
		{id: "1", at: now, key: key},
	}); err != nil {
		t.Fatal(err)
	}
	s.cfg.MaxCacheBytes = 20
	s.trimCache()
	if s.versionsLoaded {
		t.Fatal("within-limit scan loaded historical mappings")
	}
	s.cfg.MaxCacheBytes = 10
	s.trimCache()
	// The post-scan mapping must still assign the cached copy its song's heat.
	expectRetained(t, hot, true)
	expectRetained(t, cold, false)
}
