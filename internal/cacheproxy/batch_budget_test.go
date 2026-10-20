package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func batchBudgetServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	db, err := openStorageDatabase(filepath.Join(t.TempDir(), storageDatabaseName), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{cfg: cfg, usage: &usageStore{db: db}}
}

func TestBatchBudgetConcurrentReservations(t *testing.T) {
	s := batchBudgetServer(t, Config{MaxCacheBytes: 20})
	s.retained = map[string]retainedVideo{"existing": {size: 10}}
	ctx := s.WithBatchBudget(context.Background())
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			finish, err := s.reserveBatchVideo(ctx, video{key: key, size: 10})
			if err == nil {
				admitted.Add(1)
				finish(true)
			} else if !errors.Is(err, ErrBatchBudget) {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted %d", admitted.Load())
	}
}

func TestBatchBudgetReuseFailureAndEviction(t *testing.T) {
	s := batchBudgetServer(t, Config{MaxCacheBytes: 20})
	s.retained = map[string]retainedVideo{"existing": {size: 10}}
	ctx := s.WithBatchBudget(context.Background())
	reserve := func(key string, size int64, success bool) {
		t.Helper()
		finish, err := s.reserveBatchVideo(ctx, video{key: key, size: size})
		if err != nil {
			t.Fatal(err)
		}
		finish(success)
	}
	reserve("existing", 10, true)
	reserve("failed", 10, false)
	reserve("new", 10, true)
	reserve("new", 10, true)
	delete(s.retained, "existing")
	if _, err := s.reserveBatchVideo(ctx, video{key: "another", size: 1}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
}

func TestBatchBudgetUnlimitedAndOrdinaryPrefetch(t *testing.T) {
	for _, limit := range []int64{0, 1} {
		s := batchBudgetServer(t, Config{MaxCacheBytes: limit, CacheUnlimited: limit == 0})
		ctx := context.Background()
		if limit == 0 {
			ctx = s.WithBatchBudget(ctx)
		}
		finish, err := s.reserveBatchVideo(ctx, video{key: "large", size: 100})
		if err != nil {
			t.Fatal(err)
		}
		finish(true)
	}
}

func TestBatchBudgetAdmittedSongRetryAfterStop(t *testing.T) {
	s := batchBudgetServer(t, Config{MaxCacheBytes: 10})
	ctx := s.WithBatchBudget(context.Background())
	songCtx, release := WithBatchSongBudget(ctx)
	finish, err := s.reserveBatchVideo(songCtx, video{key: "original", size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.reserveBatchVideo(ctx, video{key: "other", size: 10}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
	finish(false)
	// A failed CF attempt must retain its reservation for the HKG retry.
	finish, err = s.reserveBatchVideo(songCtx, video{key: "original", size: 10})
	if err != nil {
		t.Fatal(err)
	}
	finish(false)
	if _, err := s.reserveBatchVideo(songCtx, video{key: "different", size: 20}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
	// Another song cannot borrow the admitted song's retry exemption.
	otherCtx, otherRelease := WithBatchSongBudget(ctx)
	defer otherRelease()
	if _, err := s.reserveBatchVideo(otherCtx, video{key: "original", size: 10}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
	release()
	b := ctx.Value(batchBudgetKey{}).(*batchBudget)
	if len(b.entries) != 0 {
		t.Fatal("failed song leaked its reservation")
	}
}

func TestBatchBudgetFallbackReplacesFailedVersion(t *testing.T) {
	s := batchBudgetServer(t, Config{MaxCacheBytes: 20})
	ctx := s.WithBatchBudget(context.Background())
	songCtx, release := WithBatchSongBudget(ctx)
	defer release()
	finish, err := s.reserveBatchVideo(songCtx, video{key: "cf", size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.reserveBatchVideo(ctx, video{key: "other", size: 30}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
	finish(false)
	finish, err = s.reserveBatchVideo(songCtx, video{key: "hkg", size: 20})
	if err != nil {
		t.Fatalf("failed version was double-counted: %v", err)
	}
	finish(true)
}

func TestBatchBudgetFailedPrefetchReleasesReservation(t *testing.T) {
	for _, scope := range []string{"video", "song"} {
		t.Run(scope, func(t *testing.T) {
			nextPayload := strings.Repeat("x", len(payload))
			var fail atomic.Bool
			fail.Store(true)
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if fail.Load() {
					http.Error(w, "controlled upstream failure", http.StatusServiceUnavailable)
					return
				}
				io.WriteString(w, nextPayload)
			})
			s.cfg.CacheUnlimited = false
			s.cfg.MaxCacheBytes = int64(len(payload))
			ctx := s.WithBatchBudget(context.Background())
			prefetch := func(body string) (string, error) {
				callCtx := ctx
				if scope == "song" {
					var release func()
					callCtx, release = WithBatchSongBudget(ctx)
					defer release()
				}
				return s.PrefetchSong(callCtx, "1344", videoURL(body))
			}
			if _, err := prefetch(payload); !errors.Is(err, errUpstreamDownload) {
				t.Fatalf("expected an upstream failure, got %v", err)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("caller unexpectedly canceled: %v", err)
			}
			fail.Store(false)
			// The next resource needs the entire budget released by the failed one.
			if source, err := prefetch(nextPayload); err != nil || source != "MISS" {
				t.Fatalf("next prefetch after failure: source=%q err=%v", source, err)
			}
			if _, err := prefetch(payload); !errors.Is(err, ErrBatchBudget) {
				t.Fatalf("successful prefetch did not consume the budget: %v", err)
			}
		})
	}
}

func TestBatchBudgetCanceledPrefetchKeepsReservation(t *testing.T) {
	callerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		http.Error(w, "caller canceled", http.StatusServiceUnavailable)
	})
	s.cfg.CacheUnlimited = false
	s.cfg.MaxCacheBytes = int64(len(payload))
	ctx := s.WithBatchBudget(callerCtx)
	songCtx, release := WithBatchSongBudget(ctx)
	_, err := s.PrefetchSong(songCtx, "1344", videoURL(payload))
	release()
	if err == nil || callerCtx.Err() != context.Canceled {
		t.Fatalf("expected caller cancellation, got err=%v caller=%v", err, callerCtx.Err())
	}
	// Caller cancellation keeps the charge because shared work may still finish.
	if _, err := s.reserveBatchVideo(context.WithoutCancel(ctx), video{key: "another", size: int64(len(payload))}); !errors.Is(err, ErrBatchBudget) {
		t.Fatalf("canceled prefetch released the budget: %v", err)
	}
}
