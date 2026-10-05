package cacheproxy

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func recordDemand(u *usageStore, id string, at time.Time) {
	u.record(usageEvent{id: id, at: at.UnixMilli(), summaryOnly: true})
	u.record(usageEvent{id: id, at: at.UnixMilli(), source: "http", method: "GET", demand: true})
}

func TestSongDemandReverseCompletion(t *testing.T) {
	for _, gap := range []int64{10, 35} {
		t.Run(time.Duration(gap*time.Second.Nanoseconds()).String(), func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
			if _, err := s.PrefetchSong(context.Background(), "1", videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(time.Now().Unix())
			s.usage.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			w := &pausedUsageWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(w.release) })
			done := make(chan struct{})
			const target = "http://api.udon.dance/Api/Songs/play?id=1"
			go func() { defer close(done); s.ServeHTTP(w, httptest.NewRequest("GET", target, nil)) }()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first response did not start")
			}
			clock.Add(gap)
			assertResponse(t, request(s, "GET", target, map[string]string{"Range": "bytes=0-3"}), 206, payload[:4])
			release.Do(func() { close(w.release) })
			<-done
			s.usage.flush()
			var count, firstFinished int64
			want := int64(1)
			if gap >= 30 {
				want = 2
			}
			if err := s.usage.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=1").Scan(&count); err != nil || count != want {
				t.Fatal(count, want, err)
			}
			if err := s.usage.db.QueryRow("SELECT requested_at FROM request_events WHERE source='http' ORDER BY event_id LIMIT 1").Scan(&firstFinished); err != nil || firstFinished != time.Unix(clock.Load(), 0).UnixMilli() {
				t.Fatal("short request must finish first", firstFinished, err)
			}
		})
	}
}

func TestCacheAccessSurvivesRequestCleanupAndRestart(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	old := time.Now().Add(-31 * 24 * time.Hour)
	s.usage.now = func() time.Time { return old }
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	s.usage.flush()
	cleaner := &usageStore{db: s.usage.db, retention: 30 * 24 * time.Hour}
	if n, err := cleaner.prune(time.Now()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	s.Close()
	page, err := ReadCachePage(context.Background(), cfg.StorageDir, "", "recent", 0)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].LastRequest != old.UnixMilli() {
		t.Fatal(page, err)
	}
	u, err := openUsage(filepath.Join(cfg.StorageDir, "stepstash.sqlite"), slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	var count int
	if err := u.db.QueryRow("SELECT count(*) FROM song_usage").Scan(&count); err != nil || count != 0 {
		t.Fatal("direct URL inferred song demand", count, err)
	}
	key := page.Entries[0].Key
	if err := u.write([]usageEvent{{key: key, at: old.Add(-time.Hour).UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := u.db.QueryRow("SELECT last_requested_at FROM media_access WHERE md5=?", key).Scan(&last); err != nil || last != old.UnixMilli() {
		t.Fatal("clock rollback changed access time", last, err)
	}
}

func TestSongDemandPersistsDeduplicatesAndHandlesClockRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u, err := openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000000, 0)
	for _, gap := range []time.Duration{0, 29 * time.Second, 30 * time.Second} {
		recordDemand(u, "1", base.Add(gap))
	}
	u.close()
	u, err = openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	for _, gap := range []time.Duration{-time.Hour, 31 * time.Second, 60 * time.Second} {
		recordDemand(u, "1", base.Add(gap))
	}
	u.flush()
	var score float64
	var count, last int64
	if err := u.db.QueryRow("SELECT demand_count,demand_score,last_demand_at FROM song_usage WHERE song_id=1").Scan(&count, &score, &last); err != nil {
		t.Fatal(err)
	}
	want := math.Exp2(-60.0/(60*86400)) + math.Exp2(-30.0/(60*86400)) + 1
	if count != 3 || math.Abs(score-want) > 1e-12 || last != base.Add(time.Minute).UnixMilli() {
		t.Fatalf("count=%d score=%g last=%d", count, score, last)
	}
}

func TestSongDemandConcurrentAndClose(t *testing.T) {
	u, err := openUsage(filepath.Join(t.TempDir(), "usage.sqlite"), slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); recordDemand(u, "1", time.Unix(1700000000, 0)) }()
	}
	wg.Wait()
	u.flush()
	var count int
	if err := u.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	u.close()
	u.close()
	recordDemand(u, "1", time.Now())
}

func TestOnlyExplicitSongGETAddsDemand(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.PrefetchSong(context.Background(), "1", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrefetchSong(context.Background(), "2", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"HEAD", "GET"} {
		assertResponse(t, request(s, method, videoURL(payload), nil), 200, map[string]string{"GET": payload}[method])
	}
	request(s, "HEAD", "http://api.udon.dance/Api/Songs/play?id=1", nil)
	s.usage.flush()
	var count int
	if err := s.usage.db.QueryRow("SELECT count(*) FROM song_usage").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=1", nil), 200, payload)
	s.usage.flush()
	if err := s.usage.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := s.usage.db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=2").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestUsageRejectsLegacyScoreSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE resource_usage(resource_key TEXT PRIMARY KEY)")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if u, err := openUsage(path, slog.Default(), 0); err == nil {
		u.close()
		t.Fatal("accepted old schema")
	}
}

func TestRequestDetailsPruneWithoutLosingSongDemand(t *testing.T) {
	u, err := openUsage(filepath.Join(t.TempDir(), "usage.sqlite"), slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	now := time.Now()
	cutoff := now.Add(-30 * 24 * time.Hour)
	events := make([]usageEvent, requestCleanupBatch+3)
	for i := range events {
		events[i] = usageEvent{id: "old", at: cutoff.Add(-time.Millisecond).UnixMilli()}
	}
	events = append(events, usageEvent{id: "boundary", at: cutoff.UnixMilli()}, usageEvent{id: "1", at: cutoff.Add(-time.Hour).UnixMilli(), summaryOnly: true})
	if err := u.write(events); err != nil {
		t.Fatal(err)
	}
	cleaner := &usageStore{db: u.db, retention: 30 * 24 * time.Hour}
	for _, want := range []int64{requestCleanupBatch, 3, 0} {
		if n, err := cleaner.prune(now); err != nil || n != want {
			t.Fatal(n, err)
		}
	}
	var count int
	if err := u.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := u.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestDatabaseUnavailableBlocksStartup(t *testing.T) {
	s, cfg := setup(t, nil)
	s.Close()
	dbPath := filepath.Join(cfg.StorageDir, "stepstash.sqlite")
	if err := os.WriteFile(dbPath, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := New(cfg); err == nil {
		reopened.Close()
		t.Fatal("corrupt canonical database accepted")
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal("failed startup retained ownership", err)
	}
	reopened.Close()
}

func TestRequestRetentionCleansIdleRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u, err := openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]usageEvent, requestCleanupBatch+1)
	for i := range events {
		events[i] = usageEvent{id: "old", at: time.Now().Add(-31 * 24 * time.Hour).UnixMilli()}
	}
	if err := u.write(events); err != nil {
		t.Fatal(err)
	}
	u.close()
	u, err = openUsage(path, log, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := u.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle startup did not drain backlog: %d", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestRetentionConfig(t *testing.T) {
	for _, days := range []int{-1, 36501, 0, 30, 36500} {
		cfg := DefaultConfig()
		cfg.RequestRetentionDays = days
		err := cfg.validate()
		if (err != nil) != (days < 0 || days > 36500) {
			t.Fatalf("days=%d: %v", days, err)
		}
	}
}

type pausedUsageWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *pausedUsageWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(p)
}
