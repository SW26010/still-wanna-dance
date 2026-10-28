package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fillTestCatalog(t *testing.T, s *Server, songs []CatalogSong) LocalCatalog {
	t.Helper()
	if err := s.SyncCatalog(t.Context(), Catalog{Revision: "20261028000000", Source: "test", Songs: songs}); err != nil {
		t.Fatal(err)
	}
	local, release, err := s.ReadLocalCatalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return local
}

func TestDownloadFillRetriesAdmittedResourceAfterBudgetStop(t *testing.T) {
	const firstID, otherID, retainedID = 9000001, 9000003, 9000004
	budgetStop := make(chan struct{})
	var retry atomic.Bool
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("e") != videoKey(payload) {
			t.Error("over-budget resource reached upstream")
		}
		if !retry.Load() {
			select {
			case <-budgetStop:
			case <-r.Context().Done():
				return
			}
			http.Error(w, "first candidate failed after another resource exhausted budget", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, payload)
	}, func(cfg *Config) {
		cfg.CacheUnlimited, cfg.MaxCacheBytes = false, int64(len(payload))
	})
	// The retained alias ranks this resource first, but the current catalog
	// candidate must resolve first. Retry must preserve this resource's budget.
	if err := s.SyncCatalog(t.Context(), Catalog{Revision: "20261027000000", Source: "test", Songs: []CatalogSong{{ID: retainedID, MD5: videoKey(payload)}}}); err != nil {
		t.Fatal(err)
	}
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: firstID, MD5: videoKey(payload)},
		{ID: otherID, MD5: videoKey("another resource")},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var calls []int64
	var latest DownloadFillResult
	result, err := s.NewDownloadFill(ctx, false, false).Run(local, func(ctx context.Context, id int64) (string, error) {
		mu.Lock()
		calls = append(calls, id)
		mu.Unlock()
		switch id {
		case firstID:
			return s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL(payload))
		case otherID:
			mu.Lock()
			if latest.Checked != 0 || latest.Downloaded != 0 {
				t.Errorf("admission was counted as completion: %+v", latest)
			}
			mu.Unlock()
			source, err := s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL("another resource"))
			if !errors.Is(err, ErrBatchBudget) {
				t.Errorf("expected budget refusal while first resource is unfinished, got %v", err)
			}
			close(budgetStop)
			return source, err
		case retainedID:
			retry.Store(true)
			return s.PrefetchSong(ctx, strconv.FormatInt(id, 10), videoURL(payload))
		default:
			return "", fmt.Errorf("unexpected candidate %d", id)
		}
	}, func(progress DownloadFillResult) {
		mu.Lock()
		defer mu.Unlock()
		latest = progress
	})
	if err != nil {
		t.Fatal(err)
	}
	want := DownloadFillResult{Total: 3, Checked: 2, Downloaded: 2, BudgetReached: true}
	if !reflect.DeepEqual(result, want) || !reflect.DeepEqual(latest, want) {
		t.Fatalf("result=%+v progress=%+v, want %+v", result, latest, want)
	}
	if !reflect.DeepEqual(calls, []int64{firstID, otherID, retainedID}) {
		t.Fatal("lost resource or candidate order", calls)
	}
	data, err := os.ReadFile(cfg.videoFile(videoKey(payload)))
	if err != nil || string(data) != payload {
		t.Fatal("retry did not publish the shared resource", string(data), err)
	}
}

func TestDownloadFillCancellationWaitsForRetrySettlement(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected download") })
	local := fillTestCatalog(t, s, []CatalogSong{
		{ID: 9000003, MD5: videoKey(payload)}, {ID: 9000002, MD5: videoKey(payload)},
		{ID: 9000001, MD5: videoKey("next resource")},
	})
	ctx, cancel := context.WithCancel(t.Context())
	retrying, settling, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer cancel()
	defer close(release)
	done := make(chan struct{})
	var result DownloadFillResult
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = s.NewDownloadFill(ctx, false, false).Run(local, func(ctx context.Context, id int64) (string, error) {
			switch id {
			case 9000003:
				// An early failure has no admission notification.
				return "", errors.New("address unavailable")
			case 9000002:
				close(retrying)
				<-ctx.Done()
				close(settling)
				<-release
				return "", ctx.Err()
			default:
				t.Error("resolved next resource before the current one admitted or finished")
				return "", errors.New("unexpected candidate")
			}
		}, nil)
	}()
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("fill stalled")
		}
	}
	wait(retrying)
	cancel()
	wait(settling)
	select {
	case <-done:
		t.Fatal("fill returned before the canceled retry settled")
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	wait(done)
	if !errors.Is(resultErr, context.Canceled) || result.Total != 3 || result.Checked != 0 || result.Failed != 0 || result.CurrentSongID != "9000002" {
		t.Fatalf("cancellation changed partial results: %+v err=%v", result, resultErr)
	}
}
