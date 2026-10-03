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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestUsageReverseCompletion(t *testing.T) {
	for _, gap := range []int64{10, 35} {
		t.Run(time.Duration(gap*time.Second.Nanoseconds()).String(), func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
			if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(time.Now().Unix())
			s.usage.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			w := &pausedUsageWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(w.release) })
			done := make(chan struct{})
			go func() { defer close(done); s.ServeHTTP(w, httptest.NewRequest("GET", videoURL(payload), nil)) }()
			select {
			case <-w.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first response did not start")
			}
			clock.Add(gap)
			assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=0-3"}), 206, payload[:4])
			release.Do(func() { close(w.release) })
			<-done
			s.Close()
			path := filepath.Join(cfg.StorageDir, "stepstash.sqlite")
			get, demand, _, _ := usageCounts(t, path, parsedVideo(t, s, payload).key)
			want := int64(1)
			if gap >= 30 {
				want = 2
			}
			if get != 2 || demand != want {
				t.Fatalf("reverse completion: get=%d demand=%d want=%d", get, demand, want)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var firstFinished int64
			if err := db.QueryRow("SELECT requested_at FROM request_events WHERE source='http' ORDER BY event_id LIMIT 1").Scan(&firstFinished); err != nil {
				t.Fatal(err)
			}
			if firstFinished != time.Unix(clock.Load(), 0).UnixMilli() {
				t.Fatal("short request must finish first in the detail log")
			}
		})
	}
}

func recordDemand(u *usageStore, id string, at time.Time) {
	u.record(usageEvent{id: id, at: at.UnixMilli(), demand: true, summaryOnly: true})
	u.record(usageEvent{id: id, at: at.UnixMilli(), source: "http", method: "GET", demand: true})
}

func usageCounts(t *testing.T, path, id string) (get, demand, first, last int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.QueryRow(`SELECT get_count, demand_count, first_requested_at, last_requested_at FROM resource_usage WHERE resource_key=?`, id).Scan(&get, &demand, &first, &last)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestUsagePersistsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := time.Unix(1700000000, 0)
	u, err := openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	recordDemand(u, "1", base)
	recordDemand(u, "1", base.Add(29*time.Second))
	recordDemand(u, "1", base.Add(30*time.Second))
	u.close()
	u, err = openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	recordDemand(u, "1", base.Add(31*time.Second))
	recordDemand(u, "1", base.Add(60*time.Second))
	recordDemand(u, "2", base)
	u.flush()
	var score float64
	if err := u.db.QueryRow(`SELECT demand_score FROM resource_usage WHERE resource_key='1'`).Scan(&score); err != nil {
		t.Fatal(err)
	}
	// Count requests at 0, 30 and 60 seconds, including the exact window boundary.
	wantScore := math.Exp2(-60.0/(60*86400)) + math.Exp2(-30.0/(60*86400)) + 1
	if math.Abs(score-wantScore) > 1e-12 {
		t.Fatalf("deduplicated score=%g want=%g", score, wantScore)
	}
	u.close()
	get, demand, first, last := usageCounts(t, path, "1")
	if get != 5 || demand != 3 || first != base.UnixMilli() || last != base.Add(time.Minute).UnixMilli() {
		t.Fatalf("unexpected stats: %d %d %d %d", get, demand, first, last)
	}
	get, demand, _, _ = usageCounts(t, path, "2")
	if get != 1 || demand != 1 {
		t.Fatalf("other song: %d %d", get, demand)
	}
}

func TestUsageConcurrentAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	u, err := openUsage(path, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); recordDemand(u, "1", time.Unix(1700000000, 0)) }()
	}
	wg.Wait()
	u.close()
	u.close()
	recordDemand(u, "1", time.Now()) // Requests racing shutdown must not panic.
	get, demand, _, _ := usageCounts(t, path, "1")
	if get != 100 || demand != 1 {
		t.Fatalf("concurrent stats: %d %d", get, demand)
	}
}

func TestUsageHTTPAndPrefetch(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	request(s, "HEAD", videoURL(payload), nil)
	request(s, "POST", videoURL(payload), nil)
	request(s, "GET", "http://play.udon.dance/invalid", nil)
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	otherHost := strings.Replace(videoURL(payload), "play.udon.dance", "nya.xin.moe", 1)
	assertResponse(t, request(s, "GET", otherHost, map[string]string{"Range": "bytes=0-3"}), 206, payload[:4])
	s.Close()
	get, demand, _, _ := usageCounts(t, filepath.Join(cfg.StorageDir, "stepstash.sqlite"), parsedVideo(t, s, payload).key)
	if get != 2 || demand != 1 {
		t.Fatalf("HTTP stats: %d %d", get, demand)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.StorageDir, "stepstash.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var total, heads, prefetches, ranged int
	err = db.QueryRow(`SELECT count(*), sum(method='HEAD'), sum(source='prefetch'),
sum(range_header='bytes=0-3' AND status=206 AND transferred_bytes=4 AND cache_result='HIT'
 AND outcome='completed' AND version_key<>'' AND file_bytes=36 AND elapsed_ms>=0)
FROM request_events`).Scan(&total, &heads, &prefetches, &ranged)
	if err != nil || total != 4 || heads != 1 || prefetches != 1 || ranged != 1 {
		t.Fatalf("events: %d %d %d %d: %v", total, heads, prefetches, ranged, err)
	}
}

func TestUsageRejectsLegacyScoreSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE resource_usage (resource_key TEXT PRIMARY KEY, get_count INTEGER NOT NULL,
demand_count INTEGER NOT NULL, first_requested_at INTEGER NOT NULL, last_requested_at INTEGER NOT NULL,
last_demand_at INTEGER NOT NULL);
INSERT INTO resource_usage VALUES ('1', 5, 3, 1000, 1000, 1000);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	u, err := openUsage(path, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err == nil {
		u.close()
		t.Fatal("legacy score schema accepted")
	}
}

func TestUsageCountsFailedDemand(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	request(s, "GET", videoURL(payload), nil)
	s.Close()
	get, demand, _, _ := usageCounts(t, filepath.Join(cfg.StorageDir, "stepstash.sqlite"), parsedVideo(t, s, payload).key)
	if get != 1 || demand != 1 {
		t.Fatalf("failed demand: %d %d", get, demand)
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

func TestRequestRetentionBatchesAndBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	u, err := openUsage(path, slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
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
	events = append(events, usageEvent{id: "boundary", at: cutoff.UnixMilli()},
		usageEvent{id: "recent", at: now.UnixMilli()},
		usageEvent{id: "old", at: cutoff.Add(-time.Hour).UnixMilli(), summaryOnly: true})
	if err := u.write(events); err != nil {
		t.Fatal(err)
	}
	if n, err := u.prune(now); err != nil || n != 0 {
		t.Fatalf("unlimited: %d %v", n, err)
	}
	cleaner := &usageStore{db: u.db, retention: 30 * 24 * time.Hour}
	for _, want := range []int64{requestCleanupBatch, 3, 0} {
		if n, err := cleaner.prune(now); err != nil || n != want {
			t.Fatalf("deleted=%d want=%d: %v", n, want, err)
		}
	}
	var count int
	if err := u.db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("boundary/recent: %d %v", count, err)
	}
	get, demand, _, _ := usageCounts(t, path, "old")
	if get != 1 || demand != 1 {
		t.Fatalf("summary changed: %d %d", get, demand)
	}
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
