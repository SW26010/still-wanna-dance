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

	"still-wanna-dance/internal/blocksched"
)

func TestFixedResourceFillReportsAddressStorageFailure(t *testing.T) {
	var calls atomic.Int32
	var s *Server
	s, _ = setup(t, nil, func(cfg *Config) {
		cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
			calls.Add(1)
			url := videoURL(payload)
			return url, s.ObserveSongURL(ctx, PlaybackObservation{SongID: 1, Node: "cf", URL: url})
		}
	})
	target := seedPreview(t, s, payload)
	if _, err := s.Store.db.Exec("INSERT INTO songs(id) VALUES(2); INSERT INTO song_media(song_id,md5) VALUES(2,?)", target.Resource); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.db.Exec("CREATE TRIGGER fail_address BEFORE INSERT ON media_urls BEGIN SELECT RAISE(FAIL, 'injected write failure'); END"); err != nil {
		t.Fatal(err)
	}
	_, err := s.PrefetchDownload(withExpectedMD5(t.Context(), target.Resource), 1)
	if !errors.Is(err, ErrLocalStorage) || errors.Is(err, errPlaybackUpstream) || !errors.Is(s.StorageError(), ErrLocalStorage) || calls.Load() != 1 {
		t.Fatalf("error=%v retained=%v API calls=%d", err, s.StorageError(), calls.Load())
	}
}

func TestPlaybackCheckKeepsCompletedStorageFailureOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	check := &playbackCheck{done: make(chan struct{}), err: fmt.Errorf("address write: %w", ErrLocalStorage)}
	close(check.done)
	s := &Server{}
	for range 100 {
		if _, err := s.waitSongQuery(ctx, check); !errors.Is(err, ErrLocalStorage) {
			t.Fatalf("completed storage failure was replaced: %v", err)
		}
	}
}

func TestStorageFailureStopsDownloadsButKeepsHits(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(payload)) }, func(cfg *Config) { fixtureFaultableBlocks(t, cfg) })
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
	if active != 0 {
		t.Fatal("failed flight retained capacity")
	}
}
func TestObservationStorageFailureIsRetained(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := s.Store.usage.db.Exec(`CREATE TRIGGER fail_observation BEFORE INSERT ON media_urls BEGIN SELECT RAISE(FAIL, 'disk failure'); END`); err != nil {
		t.Fatal(err)
	}

	err := s.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 42, URL: videoURL(payload), Node: "cf"})
	if !errors.Is(err, ErrLocalStorage) || !errors.Is(s.StorageError(), ErrLocalStorage) {
		t.Fatal(err)
	}
}

func TestIndexFailureStopsNewDownloads(t *testing.T) {
	var calls atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(payload)) })
	if _, err := s.Store.usage.db.Exec(`CREATE TRIGGER fail_media BEFORE INSERT ON media BEGIN SELECT RAISE(FAIL, 'index failure'); END`); err != nil {
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
	local := &os.PathError{Op: "write", Path: "block", Err: os.ErrClosed}
	upstream := fmt.Errorf("%w: failed block", errUpstreamDownload)
	for _, first := range []error{nil, upstream, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed, errBusy} {
		for _, localFirst := range []bool{false, true} {
			errorsInOrder := []error{first, local}
			if localFirst {
				errorsInOrder[0], errorsInOrder[1] = errorsInOrder[1], errorsInOrder[0]
			}
			var result error
			for _, err := range errorsInOrder {
				result = preferDownloadError(result, err)
			}
			s := &Server{Store: &Store{}}
			result = s.storageError(result)
			if !errors.Is(result, os.ErrClosed) || !errors.Is(result, ErrLocalStorage) || s.StorageError() == nil || errors.Is(result, errUpstreamDownload) {
				t.Fatalf("order=%v localFirst=%v lost local failure: %v", first, localFirst, result)
			}
		}
	}
	for _, next := range []error{nil, context.Canceled, context.DeadlineExceeded, blocksched.ErrClosed} {
		if err := preferDownloadError(upstream, next); err != upstream {
			t.Fatal("routine cleanup replaced the upstream failure", err)
		}
	}
}
