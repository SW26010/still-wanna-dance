package cacheproxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBatchBudgetConcurrentReservations(t *testing.T) {
	s := &Server{cfg: Config{MaxCacheBytes: 20}, retained: map[string]retainedVideo{"existing": {size: 10}}}
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
	s := &Server{cfg: Config{MaxCacheBytes: 20}, retained: map[string]retainedVideo{"existing": {size: 10}}}
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
		s := &Server{cfg: Config{MaxCacheBytes: limit}}
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
	s := &Server{cfg: Config{MaxCacheBytes: 10}}
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
	s := &Server{cfg: Config{MaxCacheBytes: 20}}
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
