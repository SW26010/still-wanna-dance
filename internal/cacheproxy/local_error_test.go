package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestStorageFailureStopsDownloadsButKeepsHits(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(payload)) })
	ctx := context.Background()
	if _, err := s.Prefetch(ctx, videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.cfg.tempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.tempDir(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	other := videoURL(strings.Repeat("x", len(payload)))
	if _, err := s.Prefetch(ctx, other); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	before := calls.Load()
	for range 3 {
		if _, err := s.Prefetch(ctx, other); !errors.Is(err, ErrLocalStorage) {
			t.Fatal(err)
		}
	}
	if calls.Load() != before {
		t.Fatal("restarted failed download")
	}
	if source, err := s.Prefetch(ctx, videoURL(payload)); err != nil || source != "HIT" {
		t.Fatal(source, err)
	}
	s.mu.Lock()
	active := len(s.flights)
	s.mu.Unlock()
	if active != 0 || len(s.slots) != 0 {
		t.Fatal("failed flight retained capacity")
	}
}
func TestObservationStorageFailureIsRetained(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := s.usage.db.Exec(`CREATE TRIGGER fail_observation BEFORE INSERT ON song_urls BEGIN SELECT RAISE(FAIL, 'disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err := s.ObserveSongURL(context.Background(), SongURL{SongID: 42, URL: videoURL(payload), API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: now, ObservedAt: now})
	if !errors.Is(err, ErrLocalStorage) || !errors.Is(s.StorageError(), ErrLocalStorage) {
		t.Fatal(err)
	}
}

func TestIndexFailureStopsNewDownloads(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(payload)) })
	if _, err := s.usage.db.Exec(`CREATE TRIGGER fail_media BEFORE INSERT ON media BEGIN SELECT RAISE(FAIL, 'index failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	if _, err := s.Prefetch(context.Background(), videoURL(strings.Repeat("x", len(payload)))); !errors.Is(err, ErrLocalStorage) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("download repeated after index failure", calls.Load())
	}
	if source, err := s.Prefetch(context.Background(), videoURL(payload)); source != "HIT" || err != nil {
		t.Fatal("published cache unavailable", source, err)
	}
}

func TestDownloadCleanupErrorPriority(t *testing.T) {
	local := &os.PathError{Op: "write", Path: "checkpoint", Err: os.ErrClosed}
	upstream := fmt.Errorf("%w: failed block", errUpstreamDownload)
	for _, first := range []error{nil, upstream, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed, blocksched.ErrPaused, errBusy} {
		for _, localFirst := range []bool{false, true} {
			errorsInOrder := []error{first, local}
			if localFirst {
				errorsInOrder[0], errorsInOrder[1] = errorsInOrder[1], errorsInOrder[0]
			}
			var result error
			for _, err := range errorsInOrder {
				result = preferDownloadError(result, err)
			}
			s := new(Server)
			result = s.storageError(result)
			if !errors.Is(result, os.ErrClosed) || !errors.Is(result, ErrLocalStorage) || s.StorageError() == nil || errors.Is(result, errUpstreamDownload) {
				t.Fatalf("order=%v localFirst=%v lost local failure: %v", first, localFirst, result)
			}
		}
	}
	for _, next := range []error{nil, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed, blocksched.ErrPaused} {
		if err := preferDownloadError(upstream, next); err != upstream {
			t.Fatal("routine cleanup replaced the upstream failure", err)
		}
	}
}
