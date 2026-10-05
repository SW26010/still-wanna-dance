package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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
	for _, table := range []string{"song_media"} {
		var count int
		if err := s.usage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s must not be confirmed by a canceled waiter: count=%d, err=%v", table, count, err)
		}
	}
	s.ResetQueueSongs(nil)
	s.trimCache()
	expectRetained(t, testVideoFile(t, cfg, payload), false)
}

func TestCatalogSyncPreservesActiveQueueReservations(t *testing.T) {
	for _, state := range []string{"queued", "handoff", "expired", "inactive"} {
		t.Run(state, func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
			ctx := context.Background()
			old := parsedVideo(t, s, payload).key
			current := strings.Repeat("b", 32)
			// A prefetched URL may differ from the authoritative catalog.
			if err := syncTestCatalog(s, ctx, map[string]string{"42": current}); err != nil {
				t.Fatal(err)
			}
			if state != "inactive" {
				s.SetQueueSongs([]int64{42})
			}
			if _, err := s.PrefetchSong(ctx, "42", videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			var deadline time.Time
			if state == "handoff" || state == "expired" {
				s.SetQueueSongs(nil)
				s.retentionMu.Lock()
				if state == "expired" {
					s.queueHandoffs["42"] = time.Now().Add(-time.Second)
				}
				deadline = s.queueHandoffs["42"]
				s.retentionMu.Unlock()
			}
			for range 2 {
				if err := syncTestCatalog(s, ctx, map[string]string{"42": current}); err != nil {
					t.Fatal(err)
				}
			}
			assertSongResource(t, s, "42", current)
			protected := state == "queued" || state == "handoff"
			s.retentionMu.Lock()
			reserved := s.queueReservedLocked(old)
			remembered := s.songResources["42"][old]
			after := s.queueHandoffs["42"]
			s.retentionMu.Unlock()
			if reserved != protected || remembered != protected {
				t.Fatalf("state=%s reserved=%v remembered=%v", state, reserved, remembered)
			}
			if state == "handoff" && !after.Equal(deadline) {
				t.Fatal("catalog sync changed handoff deadline")
			}
			setRetentionLimit(t, s, 1)
			s.trimCache()
			path := testVideoFile(t, cfg, payload)
			expectRetained(t, path, protected)
			if state == "handoff" {
				s.retentionMu.Lock()
				s.queueHandoffs["42"] = time.Now().Add(-time.Second)
				s.refreshHandoffsLocked(time.Now())
				s.retentionMu.Unlock()
			} else {
				s.ResetQueueSongs(nil)
			}
			s.trimCache()
			expectRetained(t, path, false)
		})
	}
}

func TestQueueReservationOutranksHotCacheAndHandsOffToPlayback(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	hot := retainedPath(t, s, cfg, "1")
	putRetained(t, hot, len(payload))
	if err := syncTestCatalog(s, context.Background(), map[string]string{"1": strings.Repeat("1", 32)}); err != nil {
		t.Fatal(err)
	}
	s.usage.startGET("1", "")
	s.usage.flush()
	priorities, err := s.songPriorities(context.Background(), []int64{1, 42}, time.Now().UnixMilli())
	if err != nil || priorities[1] <= priorities[42] {
		t.Fatalf("hot song must outrank queued song: priorities=%v err=%v", priorities, err)
	}
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
	s.ResetQueueSongs(nil)
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
	for key, id := range map[string]string{strings.Repeat("1", 32): "9", strings.Repeat("2", 32): "100"} {
		if err := syncTestCatalog(s, context.Background(), map[string]string{id: key}); err != nil {
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

// This is the real ordering from the acceptance report: departure and eviction
// run before any playback pin, followed by a probe and a separate range request.
func TestQueueDepartureBeforePlayback(t *testing.T) {
	var downloads atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.Write([]byte(payload))
	})
	setRetentionLimit(t, s, 1)
	s.SetQueueSongs([]int64{1344})
	if _, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.SetQueueSongs(nil)
	s.trimCache()
	path := testVideoFile(t, cfg, payload)
	expectRetained(t, path, true)
	probe := request(s, "HEAD", videoURL(payload), nil)
	assertResponse(t, probe, 200, "")
	s.trimCache()
	playback := request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-"})
	assertResponse(t, playback, 206, payload)
	if probe.Header().Get("X-StepStash-Cache") != "HIT" || playback.Header().Get("X-StepStash-Cache") != "HIT" || downloads.Load() != 1 {
		t.Fatalf("handoff redownloaded: probe=%v playback=%v downloads=%d", probe.Header(), playback.Header(), downloads.Load())
	}
	// Shorten only the test deadline. No explicit trim after expiry: the worker
	// must reclaim an idle removed song without waiting for its five-minute scan.
	s.retentionMu.Lock()
	s.queueHandoffs["1344"] = time.Now().Add(40 * time.Millisecond)
	s.refreshHandoffsLocked(time.Now())
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	expectRetained(t, path, false)
}

func TestQueueHandoffExpiryAndResetDuringResponse(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "reset"}[reset], func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
			setRetentionLimit(t, s, 1)
			s.SetQueueSongs([]int64{1344})
			if _, err := s.PrefetchSong(context.Background(), "1344", videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			s.SetQueueSongs(nil)
			w := &pausedUsageWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); s.ServeHTTP(w, httptest.NewRequest("GET", videoURL(payload), nil)) }()
			defer func() { close(w.release); <-done }()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("response did not start")
			}
			if reset {
				s.ResetQueueSongs(nil)
			} else {
				s.retentionMu.Lock()
				s.queueHandoffs["1344"] = time.Now().Add(-time.Second)
				s.refreshHandoffsLocked(time.Now())
				s.retentionMu.Unlock()
			}
			s.trimCache()
			expectRetained(t, testVideoFile(t, cfg, payload), true)
			// Deferred release runs before this cleanup checks eventual reclamation.
			t.Cleanup(func() {
				assertResponse(t, &responseRecorder{ResponseRecorder: w.ResponseRecorder}, 200, payload)
				expectRetained(t, testVideoFile(t, cfg, payload), false)
			})
		})
	}
}

func TestQueueHandoffOwnershipAndDeadlines(t *testing.T) {
	s := &Server{retentionWake: make(chan struct{}, 1)}
	s.SetQueueSongs([]int64{1, 2})
	s.rememberSongResourceLocked("1", "shared")
	s.rememberSongResourceLocked("2", "shared")
	s.SetQueueSongs([]int64{2})
	deadline := s.queueHandoffs["1"]
	s.SetQueueSongs([]int64{2})
	if !s.queueHandoffs["1"].Equal(deadline) {
		t.Fatal("duplicate snapshot renewed handoff")
	}
	s.refreshHandoffsLocked(deadline)
	if !s.queueReservedLocked("shared") {
		t.Fatal("expiry removed another song's reservation")
	}
	s.SetQueueSongs(nil)
	s.rememberSongResourceLocked("2", "late")
	if !s.queueReservedLocked("late") {
		t.Fatal("late resource missed handoff")
	}
	s.SetQueueSongs([]int64{2})
	if len(s.queueHandoffs) != 0 || !s.queueReservedLocked("late") {
		t.Fatal("reentry lost ownership")
	}
	s.SetQueueSongs(nil)
	s.ResetQueueSongs([]int64{1})
	if s.queueReservedLocked("late") || !s.queueReservedLocked("shared") || len(s.queueHandoffs) != 0 {
		t.Fatal("reset retained old room or lost new queue")
	}
}
