package cacheproxy

import (
	"database/sql"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTrafficPersistence(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.StorageDir = root
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.recordTraffic("GET", "HIT", "completed", 206, 50*time.Millisecond, 7) })
	}
	wg.Wait()
	s.recordTraffic("GET", "MISS", "completed", 200, time.Second, 999)
	s.recordTraffic("GET", "HIT", "canceled", 206, time.Second, 999)
	s.recordUpstream(200 * time.Millisecond)
	// The durable snapshot is visible before shutdown too.
	check := func(v TrafficStats) {
		t.Helper()
		if v.Error != "" || v.Hits != 20 || v.Misses != 1 || v.SavedBytes != 140 || v.LocalMS == nil || *v.LocalMS != 50 || v.UpstreamMS == nil || *v.UpstreamMS != 200 {
			t.Fatalf("bad persisted stats: %+v", v)
		}
	}
	check(ReadTrafficStats(root))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(ReadTrafficStats(root))
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check(s.TrafficStats())
	if v := ReadTrafficStats(t.TempDir()); v.Error != "" || v.Requests != 0 {
		t.Fatalf("new directory: %+v", v)
	}
}

func TestTrafficMigrationAndRetention(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stepstash.sqlite")
	u, err := openUsage(path, slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	u.record(usageEvent{id: "v", method: "GET", cache: "HIT", outcome: "completed", status: 206, bytes: 42})
	u.record(usageEvent{id: "v", method: "GET", cache: "HIT", outcome: "canceled", status: 206, bytes: 500})
	u.record(usageEvent{id: "v", method: "GET", cache: "MISS", outcome: "completed", status: 200})
	u.close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DROP TABLE traffic_totals`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	check := func(v TrafficStats) {
		t.Helper()
		if v.Error != "" || v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 42 || v.LocalSamples != 0 || v.LocalMS != nil {
			t.Fatalf("migration: %+v", v)
		}
	}
	check(ReadTrafficStats(root))
	cfg := DefaultConfig()
	cfg.StorageDir = root
	cfg.RequestRetentionDays = 0
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	check(s.TrafficStats())
	_, err = s.usage.db.Exec(`DELETE FROM request_events`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check(s.TrafficStats())
}

func TestExistingTrafficDoesNotReadRequestDetails(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "stats.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(trafficSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO traffic_totals VALUES (1,42,1,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	// No request_events table exists: an already migrated store must not even
	// prepare the historical aggregation query on subsequent initialization.
	if err := initializeTraffic(db); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	if err := s.loadTraffic(db); err != nil {
		t.Fatal(err)
	}
	if v := s.TrafficStats(); v.Hits != 1 || v.SavedBytes != 42 {
		t.Fatalf("existing totals changed: %+v", v)
	}
}

func TestTrafficStats(t *testing.T) {
	s := &Server{}
	if v := s.TrafficStats(); v.HitRate != nil || v.ReductionPercent != nil || v.LocalMS != nil || v.UpstreamMS != nil {
		t.Fatalf("empty statistics: %+v", v)
	}
	s.recordUpstream(100 * time.Millisecond)
	s.recordUpstream(300 * time.Millisecond)
	s.recordTraffic("GET", "MISS", "completed", 200, time.Second, 100)
	s.recordTraffic("GET", "HIT", "completed", 206, 50*time.Millisecond, 7)
	for _, sample := range []struct {
		method, outcome string
		status          int
	}{
		{"HEAD", "completed", 200}, {"GET", "failed", 200}, {"GET", "canceled", 206},
		{"GET", "aborted", 200}, {"GET", "completed", 304}, {"GET", "completed", 416},
	} {
		s.recordTraffic(sample.method, "HIT", sample.outcome, sample.status, time.Second, 1000)
	}
	v := s.TrafficStats()
	if v.Requests != 2 || v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 7 || *v.HitRate != 50 || *v.LocalMS != 50 || *v.UpstreamMS != 200 || *v.ReductionPercent != 75 {
		t.Fatalf("unexpected statistics: %+v", v)
	}
	s.recordTraffic("GET", "HIT", "completed", 200, time.Second, 100)
	if *s.TrafficStats().ReductionPercent >= 0 {
		t.Fatal("slower local response must retain a negative reduction")
	}
}

func TestTrafficStatsHTTP(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	// Wait for the shared download worker to publish before checking a hot hit.
	s.mu.Lock()
	var done []chan struct{}
	for _, f := range s.flights {
		done = append(done, f.done)
	}
	s.mu.Unlock()
	for _, ch := range done {
		<-ch
	}
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=2-5"}), 206, payload[2:6])
	request(s, "HEAD", videoURL(payload), nil)
	request(s, "POST", videoURL(payload), nil)
	request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=900-"})
	v := s.TrafficStats()
	if v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 4 || v.UpstreamSamples != 1 || v.LocalSamples != 1 || v.LocalMS == nil || v.UpstreamMS == nil {
		t.Fatalf("HTTP statistics: %+v", v)
	}
}
