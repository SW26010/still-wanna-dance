package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicationAdmissionRanksFullCacheBeforeMerge(t *testing.T) {
	for _, mode := range []string{"higher", "lower", "protected"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			oldKey := videoKey(strings.Repeat("o", len(payload)))
			seedPrioritySong(t, s, "9000001", oldKey)
			// API observation must make this song's priority available before
			// publication, even when no catalog mapping existed for it.
			now := time.Now()
			if err := s.ObserveSongURL(context.Background(), SongURL{SongID: 9000002,
				API: "https://api.udon.dance/Api/Songs/play", Node: "cf", URL: videoURL(payload),
				QueryStartedAt: now, ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			hot := "9000002"
			if mode == "lower" {
				hot = "9000001"
			}
			if err := s.usage.write([]usageEvent{{songID: hot, at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
				t.Fatal(err)
			}
			putRetained(t, cfg.videoFile(oldKey), len(payload))
			setRetentionLimit(t, s, v.size)
			if mode == "protected" {
				s.retentionMu.Lock()
				if s.protection == nil {
					s.protection = make(map[string]map[int64]blockProtection)
				}
				s.protection[oldKey] = map[int64]blockProtection{0: {start: time.Now()}}
				s.retentionMu.Unlock()
			}
			err := s.publishBlocks(context.Background(), cfg.videoFile(v.key), v, f)
			if mode == "higher" {
				if err != nil {
					t.Fatal("full cache rejected a better replacement", err)
				}
				s.runRetention(false)
				expectRetained(t, cfg.videoFile(oldKey), false)
				expectRetained(t, cfg.videoFile(v.key), true)
			} else {
				if !errors.Is(err, ErrNotRetained) || f.progress.stage == "hash" || s.StorageError() != nil {
					t.Fatal("ineligible video entered merge or became a storage fault", err, f.progress.stage)
				}
				expectRetained(t, cfg.videoFile(v.key), false)
				expectRetained(t, cfg.videoFile(oldKey), true)
				if !f.ranges.complete() {
					t.Fatal("retention denial discarded complete blocks")
				}
			}
		})
	}
}

func TestPublicationReservationCanBeDisplacedAndReleased(t *testing.T) {
	s, _ := setup(t, nil)
	setRetentionLimit(t, s, 10)
	low := video{key: videoKey("low"), size: 10}
	high := video{key: videoKey("high"), size: 10}
	seedPrioritySong(t, s, "9000001", low.key)
	seedPrioritySong(t, s, "9000002", high.key)
	first, err := s.reservePublication(context.Background(), low)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.reservePublication(context.Background(), high)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(first.ctx), ErrNotRetained) {
		t.Fatal("earlier low-priority reservation became permanent")
	}
	s.releasePublication(first)
	s.retentionMu.Lock()
	valid := len(s.publications) == 1 && s.publications[high.key] == second
	s.retentionMu.Unlock()
	if !valid {
		t.Fatal("old release removed replacement reservation")
	}
	s.releasePublication(second)
	third, err := s.reservePublication(context.Background(), low)
	if err != nil {
		t.Fatal("abandoned publication leaked its reservation", err)
	}
	s.releasePublication(third)
}

func TestPublicationRechecksProtectionBeforeRename(t *testing.T) {
	s, cfg := setup(t, nil)
	f, v := completedRangeFlight(t, s, payload)
	oldKey := videoKey(strings.Repeat("o", len(payload)))
	seedPrioritySong(t, s, "9000001", oldKey)
	seedPrioritySong(t, s, "9000002", v.key)
	putRetained(t, cfg.videoFile(oldKey), len(payload))
	setRetentionLimit(t, s, v.size)
	r, err := s.reservePublication(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releasePublication(r)
	file, err := os.CreateTemp(cfg.tempDir(), "download-publish-*.part")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, payload); err != nil {
		t.Fatal(err)
	}
	s.retentionMu.Lock()
	if s.protection == nil {
		s.protection = make(map[string]map[int64]blockProtection)
	}
	s.protection[oldKey] = map[int64]blockProtection{0: {start: time.Now()}}
	s.retentionMu.Unlock()
	if err := s.commitPublication(r, file, cfg.videoFile(v.key), v, f); !errors.Is(err, ErrNotRetained) {
		t.Fatal("publication ignored a newly protected occupant", err)
	}
	expectRetained(t, cfg.videoFile(v.key), false)
	expectRetained(t, cfg.videoFile(oldKey), true)
}

func TestZeroBudgetStreamsWithoutWholeVideoVerificationOrPublication(t *testing.T) {
	var calls atomic.Int32
	// Correct response length with deliberately wrong MD5 proves that delivery
	// remains independent of persistence-only verification.
	body := strings.Repeat("x", len(payload))
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, body)
	})
	setRetentionLimit(t, s, 0)
	for range 20 {
		assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, body)
		s.wg.Wait()
	}
	if calls.Load() != 1 || s.StorageError() != nil {
		t.Fatal("temporary bytes were redownloaded or poisoned storage", calls.Load(), s.StorageError())
	}
	expectRetained(t, cfg.videoFile(videoKey(payload)), false)
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); !errors.Is(err, ErrNotRetained) || !errors.Is(err, ErrBatchBudget) {
		t.Fatal("manual preparation claimed persistence success", err)
	}
	if calls.Load() != 1 {
		t.Fatal("manual preparation redownloaded retained blocks")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestPublicationDisplacementCancelsWaitingMerge(t *testing.T) {
	s, cfg := setup(t, nil)
	low, a := completedRangeFlight(t, s, payload)
	high, b := completedRangeFlight(t, s, strings.Repeat("h", len(payload)))
	seedPrioritySong(t, s, "9000001", a.key)
	seedPrioritySong(t, s, "9000002", b.key)
	setRetentionLimit(t, s, a.size)
	for range cap(s.localChecks) {
		s.localChecks <- struct{}{}
	}
	waitReserved := func(key string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			s.retentionMu.Lock()
			ready := s.publications[key] != nil
			s.retentionMu.Unlock()
			if ready {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("publication did not reserve before waiting for local I/O")
			}
			time.Sleep(time.Millisecond)
		}
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- s.publishBlocks(context.Background(), cfg.videoFile(a.key), a, low) }()
	waitReserved(a.key)
	go func() { second <- s.publishBlocks(context.Background(), cfg.videoFile(b.key), b, high) }()
	waitReserved(b.key)
	if err := receiveDemandEvent(t, first); !errors.Is(err, ErrNotRetained) {
		t.Fatal("displaced waiter did not release without starting merge", err)
	}
	for range cap(s.localChecks) {
		<-s.localChecks
	}
	if err := receiveDemandEvent(t, second); err != nil {
		t.Fatal(err)
	}
	if len(s.localChecks) != 0 || len(s.publications) != 0 || !low.ranges.complete() {
		t.Fatal("displacement leaked capacity or removed temporary blocks")
	}
	expectRetained(t, cfg.videoFile(a.key), false)
	expectRetained(t, cfg.videoFile(b.key), true)
}

func TestZeroBudgetQueuePreparationReportsTemporaryReadiness(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	setRetentionLimit(t, s, 0)
	s.cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(payload), nil }
	if err := s.UpdatePrefetchQueue([]int64{42}, false); err != nil {
		t.Fatal(err)
	}
	if source, err := s.PrefetchQueuePlayback(context.Background(), 42); err != nil || source != "TEMP" {
		t.Fatal("queue preparation confused readable bytes with persistence", source, err)
	}
	s.mu.Lock()
	uses := len(s.useBlocks)
	s.mu.Unlock()
	if uses == 0 {
		t.Fatal("temporary readiness ended queue usage")
	}
	expectRetained(t, cfg.videoFile(videoKey(payload)), false)
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
}
