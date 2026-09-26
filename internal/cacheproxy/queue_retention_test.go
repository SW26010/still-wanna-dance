package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestQueueReservationSurvivesCanceledDownloadWaiter(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-finish:
			w.Write([]byte(payload))
		case <-r.Context().Done():
		}
	})
	setRetentionLimit(t, s, 1)
	s.SetQueueSongs([]int64{42})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := s.PrefetchSong(ctx, "42", videoURL(payload))
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}
	// The console cancels the waiter when it moves outside the first three,
	// while retaining the song in the full pending queue.
	s.SetQueueSongs([]int64{1, 2, 3, 42})
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not cancel")
	}
	close(finish)
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.retentionMu.Lock()
		pins := s.versionPins[v.key]
		s.retentionMu.Unlock()
		if pins == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shared download did not release its pin")
		}
		time.Sleep(time.Millisecond)
	}
	s.trimCache()
	expectRetained(t, testVideoFile(t, cfg, payload), true)
	for _, table := range []string{"song_videos", "current_videos"} {
		var count int
		if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s must not be confirmed by a canceled waiter: count=%d, err=%v", table, count, err)
		}
	}
	s.SetQueueSongs(nil)
	s.trimCache()
	expectRetained(t, testVideoFile(t, cfg, payload), false)
}

func TestQueueReservationOutranksHotCacheAndHandsOffToPlayback(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	hot := retainedPath(t, s, cfg, "1")
	putRetained(t, hot, len(payload))
	s.usage.startDemand(strings.Repeat("1", 64))
	setRetentionLimit(t, s, int64(len(payload)))
	s.SetQueueSongs([]int64{42})
	if _, err := s.PrefetchSong(context.Background(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	path := testVideoFile(t, cfg, payload)
	s.trimCache()
	expectRetained(t, path, true)
	expectRetained(t, hot, false)

	// An active playback reference survives removal from the pending queue.
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, err := s.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	s.pinVideo(v)
	setRetentionLimit(t, s, 1)
	s.SetQueueSongs(nil)
	s.trimCache()
	expectRetained(t, path, true)
	s.releaseVideo(v)
	expectRetained(t, path, false)
}

func TestRetentionEqualWeightUsesNumericSongIDBeforeRecency(t *testing.T) {
	s, cfg := setup(t, nil)
	low := retainedPath(t, s, cfg, "1")
	high := retainedPath(t, s, cfg, "2")
	putRetained(t, low, 10)
	putRetained(t, high, 10)
	for key, id := range map[string]string{strings.Repeat("1", 64): "9", strings.Repeat("2", 64): "100"} {
		if _, err := s.usage.db.Exec(`INSERT INTO song_videos(song_id,version_key) VALUES (?,?)`, id, key); err != nil {
			t.Fatal(err)
		}
	}
	// Make the larger ID older, contrary to the previous recency tie-breaker.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(high, old, old); err != nil {
		t.Fatal(err)
	}
	setRetentionLimit(t, s, 10)
	s.trimCache()
	expectRetained(t, low, false)
	expectRetained(t, high, true)
}

func TestQueueProtectsExistingSharedResourceAfterRestart(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	for _, id := range []string{"42", "99"} {
		if _, err := s.PrefetchSong(context.Background(), id, videoURL(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.SetQueueSongs([]int64{42, 99})
	setRetentionLimit(t, restarted, 1)
	restarted.trimCache()
	path := testVideoFile(t, cfg, payload)
	expectRetained(t, path, true)
	restarted.SetQueueSongs([]int64{99})
	restarted.trimCache()
	expectRetained(t, path, true)
	// Shutdown clears the remaining reservation and performs final cleanup.
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	expectRetained(t, path, false)
}
