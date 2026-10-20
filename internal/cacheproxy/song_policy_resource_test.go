package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedSongEntrancesPreserveQueueClockWithoutDemand(t *testing.T) {
	var calls atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected request", 500) })
	ctx := context.Background()
	key := videoKey(payload)
	for _, id := range []string{"1", "2", "3"} {
		seedPrioritySong(t, s, id, key)
	}
	writeTestFile(t, cfg.videoFile(key), payload)
	if _, err := s.usage.db.Exec("UPDATE songs SET end=CASE song_id WHEN 1 THEN 180 WHEN 2 THEN 240 ELSE 120 END"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if err := s.SetSongPolicy(ctx, id, SongBlocked); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		w := request(s, method, "http://api.udon.dance/Api/Songs/play?id=1", nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", method, w.Code)
		}
	}
	if err := s.UpdatePrefetchQueue([]int64{-1, 1, 2, 3}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrefetchQueuePlayback(ctx, 1); !errors.Is(err, ErrSongBlocked) {
		t.Fatal(err)
	}
	if _, err := s.PrefetchSong(ctx, "2", videoURL(payload)); !errors.Is(err, ErrSongBlocked) {
		t.Fatal(err)
	}
	s.mu.Lock()
	q := s.prefetchSources["3"]
	clockOK := q.position == 3 && q.startAt.Sub(s.queueStart) == typicalDuration+7*time.Minute
	empty := len(s.demands) == 0 && len(s.useBlocks) == 0 && len(s.flights) == 0
	s.mu.Unlock()
	if !clockOK || !empty || calls.Load() != 0 {
		t.Fatal("blocking changed clock or produced work", clockOK, empty, calls.Load())
	}
	// The unblocked shared song still owns a usable local resource.
	if err := s.CheckSongAllowed(ctx, 3); err != nil {
		t.Fatal(err)
	}
	s.runRetention(true)
	expectRetained(t, cfg.videoFile(key), true)
}

func TestFavoritePublicationBypassesBudgetWithoutChangingScores(t *testing.T) {
	for _, limit := range []int64{0, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, cfg := setup(t, nil, func(c *Config) { c.AggressiveCacheReclamation = "off" })
			f, v := completedRangeFlight(t, s, payload)
			seedPrioritySong(t, s, "1", v.key)
			seedPrioritySong(t, s, "2", v.key)
			ctx := context.Background()
			before, err := s.retentionPriorities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
				t.Fatal(err)
			}
			if err := s.SetSongPolicy(ctx, 2, SongBlocked); err != nil {
				t.Fatal(err)
			}
			setRetentionLimit(t, s, limit)
			if err := s.publishBlocks(ctx, cfg.videoFile(v.key), v, f); err != nil {
				t.Fatal(err)
			}
			s.runRetention(false)
			expectRetained(t, cfg.videoFile(v.key), true)
			after, err := s.retentionPriorities(ctx)
			if err != nil || before[v.key] != after[v.key] || retainedBytes(s) != v.size {
				t.Fatal("favorite changed scores or accounting", after, err)
			}
		})
	}
}

func TestSongPolicyRetentionBelowBudgetRespectsSharingAndPins(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		t.Run(fmt.Sprint(unlimited), func(t *testing.T) {
			s, cfg := setup(t, nil)
			ctx := context.Background()
			v := video{key: videoKey(payload)}
			seedPrioritySong(t, s, "1", v.key)
			seedPrioritySong(t, s, "2", v.key)
			writeTestFile(t, cfg.videoFile(v.key), payload)
			if !unlimited {
				setRetentionLimit(t, s, 1000)
			}
			if err := s.SetSongPolicy(ctx, 1, SongBlocked); err != nil {
				t.Fatal(err)
			}
			s.runRetention(true)
			expectRetained(t, cfg.videoFile(v.key), true)
			s.pinVideo(v)
			if err := s.SetSongPolicy(ctx, 2, SongBlocked); err != nil {
				t.Fatal(err)
			}
			s.runRetention(false)
			expectRetained(t, cfg.videoFile(v.key), true)
			s.retentionMu.Lock()
			if s.protection == nil {
				s.protection = make(map[string]map[int64]blockProtection)
			}
			s.protection[v.key] = map[int64]blockProtection{0: {until: time.Now().Add(time.Hour)}}
			s.retentionMu.Unlock()
			s.releaseVideo(v)
			s.runRetention(false)
			expectRetained(t, cfg.videoFile(v.key), true)
			s.retentionMu.Lock()
			delete(s.protection, v.key)
			s.retentionMu.Unlock()
			s.runRetention(false)
			expectRetained(t, cfg.videoFile(v.key), false)
		})
	}
}

func TestFavoriteRetentionUsesRemainingBudgetAndUnmarkRestoresEviction(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	favorite, ordinary := videoKey("favorite"), videoKey("ordinary")
	seedPrioritySong(t, s, "1", favorite)
	seedPrioritySong(t, s, "2", ordinary)
	if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	putRetained(t, cfg.videoFile(favorite), 12)
	putRetained(t, cfg.videoFile(ordinary), 1)
	setRetentionLimit(t, s, 10)
	s.runRetention(false)
	expectRetained(t, cfg.videoFile(favorite), true)
	expectRetained(t, cfg.videoFile(ordinary), false)
	if retainedBytes(s) != 12 {
		t.Fatal(retainedBytes(s))
	}
	if err := s.SetSongPolicy(ctx, 1, SongNormal); err != nil {
		t.Fatal(err)
	}
	s.runRetention(false)
	expectRetained(t, cfg.videoFile(favorite), false)
}

func TestPublicationRechecksChangedSongPolicy(t *testing.T) {
	for _, policy := range []SongPolicy{SongBlocked, SongNormal} {
		t.Run(string(policy), func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			seedPrioritySong(t, s, "1", v.key)
			ctx := context.Background()
			if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
				t.Fatal(err)
			}
			if policy == SongNormal {
				setRetentionLimit(t, s, 0)
			}
			r, err := s.reservePublication(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			defer s.releasePublication(r)
			if err := s.SetSongPolicy(ctx, 1, policy); err != nil {
				t.Fatal(err)
			}
			file, err := os.CreateTemp(cfg.tempDir(), "download-publish-*.part")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(file.Name())
			defer file.Close()
			if _, err := io.WriteString(file, payload); err != nil {
				t.Fatal(err)
			}
			if err := s.commitPublication(r, file, cfg.videoFile(v.key), v, f); !errors.Is(err, ErrNotRetained) {
				t.Fatal(err)
			}
			expectRetained(t, cfg.videoFile(v.key), false)
		})
	}
}

func TestFavoriteBatchReservationContinuesAfterStopAndCountsSharedBytesOnce(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	key := videoKey(payload)
	seedPrioritySong(t, s, "1", key)
	seedPrioritySong(t, s, "2", key)
	if err := s.SetSongPolicy(ctx, 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	setRetentionLimit(t, s, 0)
	ctx = s.WithBatchBudget(ctx)
	if _, err := s.reserveBatchVideo(ctx, video{key: "ordinary", size: 1}); !errors.Is(err, ErrBatchBudget) {
		t.Fatal(err)
	}
	for range 2 {
		finish, err := s.reserveBatchVideo(ctx, video{key: key, size: 36})
		if err != nil {
			t.Fatal(err)
		}
		finish(true)
	}
	b := ctx.Value(batchBudgetKey{}).(*batchBudget)
	if len(b.entries) != 1 || b.entries[key].size != 36 {
		t.Fatal(b.entries)
	}
}

func TestBlockedTemporaryCleanupReusesProtectionWithoutFavoriteExpansion(t *testing.T) {
	w, s := testSession(t)
	s.cfg.AggressiveCacheReclamation = "off"
	d := reclamationResource(t, s, w.v)
	s.retentionMu.Lock()
	if s.versionPins == nil {
		s.versionPins = make(map[string]int)
	}
	s.versionPins[w.v.key]++
	s.retentionMu.Unlock()
	policies := map[string]resourcePolicy{w.v.key: {blocked: true}}
	s.reclaimBlocksWithPolicies(policies)
	if !d.has(0) {
		t.Fatal("policy removed active resource")
	}
	s.retentionMu.Lock()
	delete(s.versionPins, w.v.key)
	s.retentionMu.Unlock()
	s.reclaimBlocksWithPolicies(policies)
	if d.has(0) {
		t.Fatal("blocked temporary data survived")
	}
	d = reclamationResource(t, s, w.v)
	s.cfg.AggressiveCacheReclamation = "on"
	s.reclaimBlocksWithPolicies(map[string]resourcePolicy{w.v.key: {favorite: true}})
	if d.has(0) {
		t.Fatal("favorite added temporary protection")
	}
}
