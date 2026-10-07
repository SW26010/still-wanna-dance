package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
