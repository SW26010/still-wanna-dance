package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
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

			if err := s.ObserveSongURL(context.Background(), PlaybackObservation{SongID: 9000002,
				Node: "cf", URL: videoURL(payload),
			}); err != nil {
				t.Fatal(err)
			}
			hot := "9000002"
			if mode == "lower" {
				hot = "9000001"
			}
			if err := s.Store.usage.write([]usageEvent{{songID: hot, at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
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
			err := s.publishBlocks(context.Background(), cfg.videoFile(v.key), v, f, pinTestPublicationSnapshot(t, f))
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

func TestFavoritePublicationSharesBudgetAndRankingWithEviction(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprint(retain), func(t *testing.T) {
			s, cfg := setup(t, nil, func(c *Config) { c.RetainFavorites = retain })
			f, v := completedRangeFlight(t, s, payload)
			oldKey := videoKey("hot ordinary")
			seedPrioritySong(t, s, "9000001", v.key)
			seedPrioritySong(t, s, "9000002", oldKey)
			if err := s.Store.usage.write([]usageEvent{{songID: "9000002", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.SetSongPolicy(t.Context(), 9000001, SongFavorite); err != nil {
				t.Fatal(err)
			}
			putRetained(t, cfg.videoFile(oldKey), len(payload))
			setRetentionLimit(t, s, v.size)
			err := s.publishBlocks(t.Context(), cfg.videoFile(v.key), v, f, pinTestPublicationSnapshot(t, f))
			if retain && err != nil || !retain && !errors.Is(err, ErrNotRetained) {
				t.Fatal(err)
			}
			s.runRetention(false)
			expectRetained(t, cfg.videoFile(v.key), retain)
			expectRetained(t, cfg.videoFile(oldKey), !retain)
			if retainedBytes(s) != v.size {
				t.Fatal("replacement exceeded budget", retainedBytes(s))
			}
		})
	}
}

func TestFavoritePublicationReservationsCannotAccumulatePastBudget(t *testing.T) {
	s, _ := setup(t, nil)
	setRetentionLimit(t, s, 10)
	low, high := video{key: videoKey("low favorite"), size: 10}, video{key: videoKey("high favorite"), size: 10}
	for id, key := range map[int64]string{9000001: low.key, 9000002: high.key} {
		seedPrioritySong(t, s, fmt.Sprint(id), key)
		if err := s.Store.SetSongPolicy(t.Context(), id, SongFavorite); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.reservePublication(t.Context(), low)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releasePublication(first)
	second, err := s.reservePublication(t.Context(), high)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releasePublication(second)
	if !errors.Is(context.Cause(first.ctx), ErrNotRetained) {
		t.Fatal("lower ranked favorite reservation bypassed budget")
	}
}

func TestPublicationRechecksProtectionBeforeRename(t *testing.T) {
	for _, initiallyFull := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially-full=%t", initiallyFull), func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			oldKey := videoKey(strings.Repeat("o", len(payload)))
			seedPrioritySong(t, s, "9000001", oldKey)
			seedPrioritySong(t, s, "9000002", v.key)
			if initiallyFull {
				putRetained(t, cfg.videoFile(oldKey), len(payload))
			}
			setRetentionLimit(t, s, v.size)
			r, err := s.reservePublication(context.Background(), v)
			if err != nil {
				t.Fatal(err)
			}
			defer s.releasePublication(r)
			if !initiallyFull {
				// An import/reconciliation can consume space while merge I/O runs.
				putRetained(t, cfg.videoFile(oldKey), len(payload))
				s.runRetention(true)
			}
			file := stagedPublicationFile(t, cfg, payload)
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
		})
	}
}

func TestZeroBudgetStreamsWithoutWholeVideoVerificationOrPublication(t *testing.T) {
	var calls atomic.Int32
	// Correct response length with deliberately wrong MD5 proves that delivery
	// remains independent of persistence-only verification.
	body := strings.Repeat("x", len(payload))
	const blockedStage = "not a directory"
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		serveFixtureContent(w, r, body)
	}, func(cfg *Config) {
		cfg.CacheUnlimited = false
		cfg.VideoBudgetBytes = 0
		writeTestFile(t, cfg.publishDir(), blockedStage)
	})
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
	if got, err := os.ReadFile(cfg.publishDir()); err != nil || string(got) != blockedStage {
		t.Fatal("streaming changed the blocked publication location", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, cfg.tempDir())
}

func waitForPublicationReservation(t *testing.T, s *Server, key string) {
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
	first, second := make(chan error, 1), make(chan error, 1)
	lowSnapshot := pinTestPublicationSnapshot(t, low)
	highSnapshot := pinTestPublicationSnapshot(t, high)
	go func() { first <- s.publishBlocks(context.Background(), cfg.videoFile(a.key), a, low, lowSnapshot) }()
	waitForPublicationReservation(t, s, a.key)
	go func() { second <- s.publishBlocks(context.Background(), cfg.videoFile(b.key), b, high, highSnapshot) }()
	waitForPublicationReservation(t, s, b.key)
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

func TestCompleteDownloadKeepsSnapshotWhileWaitingToPublish(t *testing.T) {
	body := strings.Repeat("r", int(2*rangeBlockSize))
	s, cfg := setup(t, nil, func(c *Config) {
		c.LocalProtectionGrace = 0
		c.ResourceTransports = func(string) []http.RoundTripper {
			return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				return fixtureRangeResponse(r, body), nil
			})}
		}
	})
	for range cap(s.localChecks) {
		s.localChecks <- struct{}{}
	}
	unblock := sync.OnceFunc(func() {
		for range cap(s.localChecks) {
			<-s.localChecks
		}
	})
	defer unblock()

	v := parsedVideo(t, s, body)
	// GET releases each block as it is delivered. Hold a preparation demand
	// until the complete snapshot is reserved, even with zero protection grace.
	_, release := addTestDemand(t, s, t.Context(), v, false)
	defer release()
	assertResponse(t, request(s, "GET", videoURL(body), nil), http.StatusOK, body)
	waitForPublicationReservation(t, s, v.key)
	release()
	s.mu.Lock()
	stillNeeded := s.useBlocks[useBlockID{v.key, 0}] != 0 || s.useBlocks[useBlockID{v.key, 1}] != 0
	s.mu.Unlock()
	if stillNeeded {
		t.Fatal("fixture still protects downloaded blocks through a demand")
	}
	s.retentionMu.Lock()
	local := s.localResources[v.key]
	s.retentionMu.Unlock()
	if local == nil {
		t.Fatal("flight disappeared while publication was waiting")
	}
	s.reclaimLocalBlocks()
	if !local.has(0) || !local.has(1) {
		t.Fatal("reclamation detached blocks while the flight was publishing")
	}
	assertResponse(t, request(s, "GET", videoURL(body), map[string]string{"Range": "bytes=0-3"}),
		http.StatusPartialContent, body[:4])
	unblock()
	s.wg.Wait()
	got, err := os.ReadFile(cfg.videoFile(v.key))
	if err != nil || string(got) != body {
		t.Fatalf("publication lost its already-complete snapshot: %v (%d bytes)", err, len(got))
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestSeparatedRangesCanFinishWithoutPublishing(t *testing.T) {
	body := strings.Repeat("a", int(rangeBlockSize)) + "tail"
	var calls atomic.Int32
	s, cfg := setup(t, nil, func(c *Config) {
		c.LocalProtectionGrace = 0
		c.ResourceTransports = func(string) []http.RoundTripper {
			return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return fixtureRangeResponse(r, body), nil
			})}
		}
	})
	target := videoURL(body)
	assertResponse(t, request(s, "GET", target, map[string]string{
		"Range": fmt.Sprintf("bytes=0-%d", rangeBlockSize-1),
	}), http.StatusPartialContent, body[:rangeBlockSize])
	s.wg.Wait()
	s.reclaimLocalBlocks()
	assertNoPartial(t, cfg.tempDir())

	assertResponse(t, request(s, "GET", target, map[string]string{
		"Range": fmt.Sprintf("bytes=%d-", rangeBlockSize),
	}), http.StatusPartialContent, "tail")
	s.wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("publication refetched a block without a demand", calls.Load())
	}
	expectRetained(t, cfg.videoFile(videoKey(body)), false)
}

func TestZeroBudgetQueuePreparationReportsTemporaryReadiness(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
	setRetentionLimit(t, s, 0)
	s.cfg.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(payload), nil }
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
