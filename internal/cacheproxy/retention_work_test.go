package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Hold the only write/policy connection. A fresh query would wait until its
// context expires; a valid immutable snapshot remains immediately available.
func cachedResourcePolicies(t testing.TB, s *Server) map[string]resourcePolicy {
	t.Helper()
	conn, err := s.Store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	policies, err := s.resourcePolicies(ctx)
	if err != nil {
		t.Fatal("unchanged policies were queried again", err)
	}
	return policies
}

func TestRetentionReusesUnchangedResourcePolicies(t *testing.T) {
	s := retentionChangeEngine(t)
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Store.SyncCatalog(t.Context(), testCandidate("20261115000000", CatalogSong{ID: 1, MD5: key})); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SetSongPolicy(t.Context(), 1, SongFavorite); err != nil {
		t.Fatal(err)
	}
	first, err := s.resourcePolicies(t.Context())
	if err != nil || !first[key].favorite {
		t.Fatal("initial policy", first, err)
	}
	next := cachedResourcePolicies(t, s)
	if next[key] != first[key] {
		t.Fatal("cached policy changed", next, first)
	}
	// Cache reuse still respects the caller's cancellation boundary.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.resourcePolicies(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cached policy ignored cancellation", err)
	}
}

func TestUnchangedSongMappingKeepsRetentionPolicies(t *testing.T) {
	s := retentionChangeEngine(t)
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.Store.SyncCatalog(t.Context(), testCandidate("20261115000000", CatalogSong{ID: 1, MD5: key})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resourcePolicies(t.Context()); err != nil {
		t.Fatal(err)
	}
	requireRetentionWake(t, s, true)
	v := video{key: key}
	s.pinVideo(v) // Isolate mapping notification from the independent last-pin wake.
	if err := s.recordSongVideo(t.Context(), "1", v); err != nil {
		t.Fatal(err)
	}
	requireRetentionWake(t, s, false)
	cachedResourcePolicies(t, s)
	s.releaseVideo(v)
	requireRetentionWake(t, s, true)
}

func TestGETConsumptionReclaimsOnlyAffectedResource(t *testing.T) {
	for _, mode := range []string{"zero", "positive", "unlimited"} {
		t.Run(mode, func(t *testing.T) {
			s := retentionChangeEngine(t)
			s.cfg.CacheUnlimited = mode == "unlimited"
			s.cfg.VideoBudgetBytes = 100
			if mode == "zero" {
				s.cfg.VideoBudgetBytes = 0
			}
			s.cfg.LocalProtectionGrace = 0
			const affected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			const unrelated = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			if err := s.Store.SyncCatalog(t.Context(), testCandidate("20261115000000", CatalogSong{ID: 1, MD5: affected}, CatalogSong{ID: 2, MD5: unrelated})); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{1, 2} {
				if err := s.Store.SetSongPolicy(t.Context(), id, SongBlocked); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.resourcePolicies(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Both resources are eligible for blocked cleanup. With spare or
			// unlimited capacity, only the requested resource should be checked.
			// Zero capacity still needs independent global video reclamation.
			dir := t.TempDir()
			for _, key := range []string{affected, unrelated} {
				path := filepath.Join(dir, key+".mp4")
				putRetained(t, path, 10)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				s.retainVideoLocked(key, &retainedVideo{path: path, key: key, size: info.Size(), modified: info.ModTime().UnixNano()})
			}
			v := video{key: affected, size: 2 * rangeBlockSize}
			local := reclamationResource(t, s, v)
			other := &localResource{video: video{key: unrelated, size: rangeBlockSize}, storage: newBlockFiles(t.TempDir(), rangeBlockSize, rangeBlockSize, nil)}
			if err := other.storage.store(0, bytes.Repeat([]byte{1}, int(rangeBlockSize))); err != nil {
				t.Fatal(err)
			}
			s.localResources[unrelated] = other
			s.retentionPending = retentionWork{}
			ctx := context.WithValue(t.Context(), httpGETKey{}, true)
			d, release := addTestDemand(t, s, ctx, v, true)
			defer release()
			s.pinVideo(v)
			g := &getConsumption{s: s, demand: d, size: v.size}
			g.start(http.StatusOK, nil)
			g.accept(rangeBlockSize)
			s.runRequestedRetention()
			if !local.has(1) || !other.has(0) {
				t.Fatal("consumption reclaimed unread or unrelated blocks")
			}
			g.accept(rangeBlockSize)
			s.runRequestedRetention()
			expectRetained(t, filepath.Join(dir, affected+".mp4"), true)
			s.releaseVideo(v)
			s.runRequestedRetention()
			expectRetained(t, filepath.Join(dir, affected+".mp4"), false)
			expectRetained(t, filepath.Join(dir, unrelated+".mp4"), mode != "zero")
			if local.has(0) || local.has(1) || !other.has(0) {
				t.Fatal("resource cleanup crossed the requested scope")
			}
			s.retentionMu.Lock()
			s.requestRetentionLocked()
			s.retentionMu.Unlock()
			s.runRequestedRetention()
			expectRetained(t, filepath.Join(dir, unrelated+".mp4"), false)
			if other.has(0) {
				t.Fatal("full policy pass missed unrelated blocked blocks")
			}
		})
	}
}

func TestConsumptionArmsProtectionExpiryWithoutFullRetention(t *testing.T) {
	w, s := testSession(t)
	w.v.size = 2 * rangeBlockSize
	s.cfg.LocalProtectionGrace = 30 * time.Millisecond
	s.ctx, s.cancel = context.WithCancel(t.Context())
	s.retentionWake, s.retentionDone = make(chan struct{}, 1), make(chan struct{})
	// The worker needs no business database when the immutable policy snapshot
	// is already current. Exercise the real timer and exact block-file cleanup.
	s.policyCache.policies = map[string]resourcePolicy{}
	local := reclamationResource(t, s, w.v)
	first := localBlockPath(t, local.storage, 0)
	d, release := addTestDemand(t, s, context.WithValue(t.Context(), httpGETKey{}, true), w.v, true)
	defer release()
	g := &getConsumption{s: s, demand: d, size: w.v.size}
	g.start(http.StatusOK, nil)
	g.accept(rangeBlockSize)
	go s.retentionLoop()
	defer func() { s.cancel(); <-s.retentionDone }()
	expectRetained(t, first, false)
	if local.has(0) || !local.has(1) {
		t.Fatal("consumption timer missed its tail or removed live protection")
	}
}

// Empty inventory and unlimited capacity isolate repeated policy computation.
// Each iteration is a coalesced retention pass, not a cost charged per GET block.
func BenchmarkRetentionUnchangedPolicies(b *testing.B) {
	for _, marked := range []int{1000, 10000} {
		b.Run(fmt.Sprint(marked), func(b *testing.B) {
			s := retentionChangeEngine(b)
			if _, err := s.Store.db.Exec(`
WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<?)
INSERT INTO songs(id) SELECT n FROM ids;
INSERT INTO media(md5) SELECT printf('%032x',id) FROM songs;
INSERT INTO song_media(song_id,md5) SELECT id,printf('%032x',id) FROM songs;
INSERT INTO song_preferences(song_id,policy) SELECT id,CASE WHEN id%2=0 THEN 'favorite' ELSE 'blocked' END FROM songs;`, marked); err != nil {
				b.Fatal(err)
			}
			if err := s.trimCachePass(false); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := s.trimCachePass(false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Include the real consumption-to-retention path. Unrelated inventory and live
// protection grow together; the affected resource still has only two blocks.
func BenchmarkGETConsumptionRetention(b *testing.B) {
	for _, unrelated := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprint(unrelated), func(b *testing.B) {
			s := retentionChangeEngine(b)
			if _, err := s.resourcePolicies(b.Context()); err != nil {
				b.Fatal(err)
			}
			s.retained = make(map[string]retainedVideo, unrelated)
			s.protection = make(map[string]map[int64]blockProtection, unrelated)
			now := time.Now()
			for i := range unrelated {
				key := fmt.Sprintf("%032x", i+1)
				s.retained[key] = retainedVideo{key: key, size: 1}
				s.retainedBytes++
				s.protection[key] = map[int64]blockProtection{0: {start: now}}
			}
			v := video{key: "consumed", size: 2 * rangeBlockSize}
			ctx := context.WithValue(b.Context(), httpGETKey{}, true)
			d, release, err := s.addDemand(ctx, v, true)
			if err != nil {
				b.Fatal(err)
			}
			defer release()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				g := &getConsumption{s: s, demand: d, size: v.size}
				g.start(http.StatusOK, nil)
				g.accept(rangeBlockSize)
				s.runRequestedRetention()
				s.mu.Lock()
				s.useBlocks[useBlockID{v.key, 0}]++
				d.firstBlock = 0
				s.mu.Unlock()
			}
		})
	}
}
