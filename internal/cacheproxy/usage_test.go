package cacheproxy

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
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
			clock.Store(1700000000)
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
			path := filepath.Join(cfg.SongsDir, ".stepstash-usage.sqlite")
			get, demand, _, _ := usageCounts(t, path, "1344")
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
	err = db.QueryRow(`SELECT get_count, demand_count, first_requested_at, last_requested_at FROM song_usage WHERE song_id=?`, id).Scan(&get, &demand, &first, &last)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestUsagePersistsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := time.Unix(1700000000, 0)
	u, err := openUsage(path, log)
	if err != nil {
		t.Fatal(err)
	}
	recordDemand(u, "1", base)
	recordDemand(u, "1", base.Add(29*time.Second))
	recordDemand(u, "1", base.Add(30*time.Second))
	u.close()
	u, err = openUsage(path, log)
	if err != nil {
		t.Fatal(err)
	}
	recordDemand(u, "1", base.Add(31*time.Second))
	recordDemand(u, "1", base.Add(60*time.Second))
	recordDemand(u, "2", base)
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
	u, err := openUsage(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	get, demand, _, _ := usageCounts(t, filepath.Join(cfg.SongsDir, ".stepstash-usage.sqlite"), "1344")
	if get != 2 || demand != 1 {
		t.Fatalf("HTTP stats: %d %d", get, demand)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.SongsDir, ".stepstash-usage.sqlite"))
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

func TestUsageUpgradesSummaryOnlyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE song_usage (song_id TEXT PRIMARY KEY, get_count INTEGER NOT NULL,
demand_count INTEGER NOT NULL, first_requested_at INTEGER NOT NULL, last_requested_at INTEGER NOT NULL,
last_demand_at INTEGER NOT NULL);
INSERT INTO song_usage VALUES ('1', 5, 3, 1000, 1000, 1000);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	u, err := openUsage(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	recordDemand(u, "1", time.UnixMilli(32000))
	u.close()
	get, demand, first, last := usageCounts(t, path, "1")
	if get != 6 || demand != 4 || first != 1000 || last != 32000 {
		t.Fatalf("migrated stats: %d %d %d %d", get, demand, first, last)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM request_events").Scan(&count); err != nil || count != 1 {
		t.Fatalf("must not invent historical events: %d %v", count, err)
	}
}

func TestUsageCountsFailedDemand(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	request(s, "GET", videoURL(payload), nil)
	s.Close()
	get, demand, _, _ := usageCounts(t, filepath.Join(cfg.SongsDir, ".stepstash-usage.sqlite"), "1344")
	if get != 1 || demand != 1 {
		t.Fatalf("failed demand: %d %d", get, demand)
	}
}

func TestUsageUnavailableDoesNotBlockVideo(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	s.Close()
	cfg.StatsPath = filepath.Join(t.TempDir(), "bad.sqlite")
	if err := os.WriteFile(cfg.StatsPath, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.usage != nil {
		t.Fatal("corrupt database should disable statistics")
	}
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
}
