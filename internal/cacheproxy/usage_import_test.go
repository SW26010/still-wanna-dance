package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func historyAt(id, at int64) *SongUsageImport {
	h := &SongUsageImport{}
	h.Observe(id, at)
	return h
}

func TestHistoricalUsageMatchesNativeDemand(t *testing.T) {
	db, err := openFixtureDatabase(filepath.Join(t.TempDir(), "storage.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u := &usageStore{db: db}
	h := &SongUsageImport{}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, gap := range []int64{0, 29999, 30000, 45000, 60000, demandHalfLife.Milliseconds()} {
		h.Observe(1, start+gap)
		if err = u.write([]usageEvent{{songID: "1", at: start + gap, summaryOnly: true}}); err != nil {
			t.Fatal(err)
		}
	}
	var row songUsage
	if err = db.QueryRow("SELECT demand_count,demand_score,last_demand_at FROM song_usage WHERE song_id=1").Scan(&row.count, &row.score, &row.last); err != nil {
		t.Fatal(err)
	}
	if row != h.rows[1] || h.Events != 4 || h.Duplicates != 2 {
		t.Fatal(row, h)
	}
	if math.Abs(retentionScore(row.score, row.last, row.last+demandHalfLife.Milliseconds())-row.score/2) > 1e-12 {
		t.Fatal("query decay")
	}
	h.Observe(2, start)
	if h.Songs() != 2 || h.Events != 5 {
		t.Fatal("dedup crossed songs")
	}
}

func TestHistoricalUsageReplacementRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	created, err := CreateStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	old := historyAt(99, start)
	if err := ImportSongUsage(ctx, storagePath(root), old, false); err != nil {
		t.Fatal(err)
	}
	db, err := openFixtureDatabase(storagePath(root), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO media(md5) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
		INSERT INTO song_media VALUES (99,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
		CREATE TRIGGER fail_import BEFORE INSERT ON song_usage WHEN new.id=1 BEGIN SELECT RAISE(ABORT,'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	h := historyAt(1, start)
	if err = replaceSongUsage(ctx, db, h, false); !errors.Is(err, ErrSongUsageExists) {
		t.Fatal(err)
	}
	if err = replaceSongUsage(ctx, db, h, true); err == nil {
		t.Fatal("failure not propagated")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&count); err != nil || count != 1 {
		t.Fatal("old rows lost", count, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM songs WHERE id=1").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial placeholder", count, err)
	}
	if _, err = db.Exec("DROP TRIGGER fail_import"); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "videos", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4")
	if err = os.MkdirAll(filepath.Dir(video), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(video, []byte("unchanged-media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ImportSongUsage(ctx, storagePath(root), h, true); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&count); err != nil || count != 0 {
		t.Fatal("not whole-table replacement")
	}
	if err = db.QueryRow("SELECT count(*) FROM song_media WHERE song_id=99").Scan(&count); err != nil || count != 1 {
		t.Fatal("mapping modified")
	}
	if err = db.QueryRow("SELECT count(*) FROM song_media WHERE song_id=1").Scan(&count); err != nil || count != 0 {
		t.Fatal("invented mapping")
	}
	if data, err := os.ReadFile(video); err != nil || string(data) != "unchanged-media" {
		t.Fatal("video modified", err)
	}
	if err = ImportSongUsage(ctx, storagePath(root), &SongUsageImport{}, true); err == nil {
		t.Fatal("empty replacement allowed")
	}
	db.Close()
	db, err = openFixtureDatabase(storagePath(root), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var score float64
	var at int64
	if err = db.QueryRow("SELECT demand_count,demand_score,last_demand_at FROM song_usage WHERE song_id=1").Scan(&count, &score, &at); err != nil || count != 1 || score != 1 || at != start {
		t.Fatal("restart", count, score, at, err)
	}
}

func TestHistoricalUsageConcurrentGETAndImports(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	ctx := context.Background()
	if _, err := s.PrefetchSong(ctx, "1", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-120 * 24 * time.Hour).UnixMilli()
	h := historyAt(1, start)
	// The pending real demand is accepted before the import's final check.
	s.usage.startGET("99", "")
	if err := s.ImportSongUsage(ctx, h, false); !errors.Is(err, ErrSongUsageExists) {
		t.Fatal("pending GET overwritten", err)
	}
	if err := s.ImportSongUsage(ctx, h, true); err != nil {
		t.Fatal(err)
	}
	// Concurrent repeat requests cannot bypass the empty-table check.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ImportSongUsage(ctx, h, false); !errors.Is(err, ErrSongUsageExists) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=1", nil), 200, payload)
	s.usage.flush()
	var count int
	var score float64
	var last int64
	if err := s.usage.db.QueryRow("SELECT demand_count,demand_score,last_demand_at FROM song_usage WHERE song_id=1").Scan(&count, &score, &last); err != nil || count != 2 || math.Abs(score-1.25) > 1e-5 {
		t.Fatal(count, score, last, err)
	}
	// Existing batch and eviction consumers read the same imported accumulator.
	p, err := s.resourcePriorities(ctx, map[string]string{"1": "same", "2": "same"}, last)
	if err != nil || p["same"].Score < score {
		t.Fatal(p, err)
	}
}

func TestHistoricalUsageResourceRankingAndEviction(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	seedPrioritySong(t, s, "1", strings.Repeat("1", 32))
	seedPrioritySong(t, s, "2", strings.Repeat("2", 32))
	seedPrioritySong(t, s, "3", strings.Repeat("1", 32))
	h := historyAt(2, now-2*demandHalfLife.Milliseconds())
	h.Observe(1, now)
	h.Observe(3, now)
	if err := s.ImportSongUsage(ctx, h, false); err != nil {
		t.Fatal(err)
	}
	ranks, err := s.resourcePriorities(ctx, map[string]string{"1": strings.Repeat("1", 32), "2": strings.Repeat("2", 32), "3": strings.Repeat("1", 32)}, now)
	if err != nil || ranks[strings.Repeat("1", 32)].Score != 2 || ranks[strings.Repeat("2", 32)].Score != 0.25 {
		t.Fatal("shared resource/batch ranks", ranks, err)
	}
	hot, cold := cfg.videoFile(strings.Repeat("1", 32)), cfg.videoFile(strings.Repeat("2", 32))
	putRetained(t, hot, 10)
	putRetained(t, cold, 10)
	setRetentionLimit(t, s, 10)
	s.trimCache()
	expectRetained(t, hot, true)
	expectRetained(t, cold, false)
}

func TestHistoricalUsageIdleLockAndEmptyRollback(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	db, err := openFixtureDatabase(storagePath(root), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TRIGGER reject_all BEFORE INSERT ON song_usage BEGIN SELECT RAISE(ABORT,'full'); END"); err != nil {
		t.Fatal(err)
	}
	if err = replaceSongUsage(ctx, db, historyAt(1, time.Now().UnixMilli()), false); err == nil {
		t.Fatal("ignored write failure")
	}
	var count int
	db.QueryRow("SELECT count(*) FROM songs").Scan(&count)
	if count != 0 {
		t.Fatal("partial empty import")
	}
	unlock, err := lockDirectory(databaseLeasePath(storagePath(root)))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = ImportSongUsage(ctx, storagePath(root), historyAt(1, time.Now().UnixMilli()), true); err == nil {
		t.Fatal("database lock bypassed")
	}
}

func TestHistoricalUsageFullAndForeignKeyRollback(t *testing.T) {
	for _, failure := range []string{"full", "foreign-key"} {
		t.Run(failure, func(t *testing.T) {
			db, err := openFixtureDatabase(storagePath(t.TempDir()), true)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			at := time.Now().UnixMilli()
			if err = replaceSongUsage(ctx, db, historyAt(99, at), false); err != nil {
				t.Fatal(err)
			}
			h := historyAt(1, at)
			code := sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
			if failure == "full" {
				var pages int
				if err = db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
					t.Fatal(err)
				}
				for id := int64(2); id < 10000; id++ {
					h.Observe(id, at)
				}
				code = sqlite3.SQLITE_FULL
			} else {
				// Simulate failure to establish the foreign-key prerequisite.
				if _, err = db.Exec("CREATE TRIGGER skip_placeholder BEFORE INSERT ON songs WHEN new.id=1 BEGIN SELECT RAISE(IGNORE); END"); err != nil {
					t.Fatal(err)
				}
			}
			err = replaceSongUsage(ctx, db, h, true)
			var sqliteErr *sqlite.Error
			if !errors.As(err, &sqliteErr) || sqliteErr.Code() != code {
				t.Fatalf("expected SQLite code %d, got %v", code, err)
			}
			for _, table := range []string{"songs", "song_usage"} {
				var count, id int
				key := "song_id"
				if table == "songs" {
					key = "id"
				}
				if err = db.QueryRow("SELECT count(*), min("+key+") FROM "+table).Scan(&count, &id); err != nil || count != 1 || id != 99 {
					t.Fatal("rollback lost old data or kept partial data", table, count, id, err)
				}
			}
		})
	}
}
