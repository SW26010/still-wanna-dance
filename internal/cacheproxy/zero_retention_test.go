package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestZeroRetentionReusesQueueVideoUntilHandoffExpires(t *testing.T) {
	var downloads atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write([]byte(payload))
	})
	setRetentionLimit(t, s, 0)
	s.SetQueueSongs([]int64{42})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	path := testVideoFile(t, cfg, payload)
	for range 2 {
		assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
		s.trimCache()
		expectRetained(t, path, true)
	}
	if downloads.Load() != 1 {
		t.Fatalf("protected cache downloaded %d times", downloads.Load())
	}
	s.SetQueueSongs(nil)
	s.trimCache()
	expectRetained(t, path, true)
	// Shorten this test's handoff; exercise the worker's timer without a new request.
	s.retentionMu.Lock()
	s.queueHandoffs["42"] = time.Now().Add(30 * time.Millisecond)
	s.refreshHandoffsLocked(time.Now())
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	expectRetained(t, path, false)
}

func TestZeroRetentionProtectsReferencesAndReclaimsExistingVideos(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("1", 32)
	path := cfg.videoFile(key)
	other := cfg.videoFile(strings.Repeat("2", 32))
	unknown := filepath.Join(cfg.videosDir(), "personal.mp4")
	for _, p := range []string{path, other, unknown} {
		putRetained(t, p, 10)
	}
	v := video{key: key}
	s.pinVideo(v)
	s.pinVideo(v)
	setRetentionLimit(t, s, 0)
	s.trimCache()
	expectRetained(t, path, true)
	expectRetained(t, other, false)
	expectRetained(t, unknown, true)
	s.releaseVideo(v)
	s.trimCache()
	expectRetained(t, path, true)
	s.releaseVideo(v)
	expectRetained(t, path, false)
}

func TestZeroRetentionStartupAndUnlimited(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero", true: "unlimited"}[unlimited], func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.StorageDir = t.TempDir()
			initial, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}
			path := cfg.videoFile(strings.Repeat("a", 32))
			putRetained(t, path, 10)
			if unlimited {
				cfg.CacheUnlimited = true
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			expectRetained(t, path, unlimited)
		})
	}
}

func TestZeroBatchBudgetRejectsNewDownloadsButAllowsProtectedReuse(t *testing.T) {
	s := &Server{cfg: Config{MaxCacheBytes: 0}, retained: map[string]retainedVideo{"protected": {size: 10}}}
	ctx := s.WithBatchBudget(context.Background())
	finish, err := s.reserveBatchVideo(ctx, video{key: "protected", size: 10})
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	if _, err := s.reserveBatchVideo(ctx, video{key: "new", size: 1}); !errors.Is(err, ErrBatchBudget) {
		t.Fatalf("zero budget accepted a new download: %v", err)
	}
}
