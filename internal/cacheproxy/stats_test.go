package cacheproxy

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
)

func TestTrafficPersistence(t *testing.T) {
	root := t.TempDir()
	cfg := fixtureConfig()
	cfg.StorageDir = root
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.recordTraffic("GET", "HIT", "completed", 206, 7) })
	}
	wg.Wait()
	s.recordTraffic("GET", "MISS", "completed", 200, 999)
	s.recordTraffic("GET", "HIT", "canceled", 206, 999)

	// The durable snapshot is visible before shutdown too.
	check := func(v TrafficStats) {
		t.Helper()
		if v.Error != "" || v.Hits != 20 || v.Misses != 1 || v.SavedBytes != 140 {
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

func TestTrafficPersistenceAndRetention(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "storage.sqlite")
	u, err := openUsage(path, slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.db.Exec(`UPDATE traffic_totals SET hits=1,misses=1,saved_bytes=42 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	u.close()
	check := func(v TrafficStats) {
		t.Helper()
		if v.Error != "" || v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 42 {
			t.Fatalf("migration: %+v", v)
		}
	}
	check(ReadTrafficStats(root))
	cfg := fixtureConfig()
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
	// Initialization preserves totals independently of request detail retention.
	if err := initializeStorage(db); err != nil {
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
	if v := s.TrafficStats(); v.HitRate != nil {
		t.Fatalf("empty statistics: %+v", v)
	}

	s.recordTraffic("GET", "MISS", "completed", 200, 100)
	s.recordTraffic("GET", "HIT", "completed", 206, 7)
	for _, sample := range []struct {
		method, outcome string
		status          int
	}{
		{"HEAD", "completed", 200}, {"GET", "failed", 200}, {"GET", "canceled", 206},
		{"GET", "aborted", 200}, {"GET", "completed", 304}, {"GET", "completed", 416},
	} {
		s.recordTraffic(sample.method, "HIT", sample.outcome, sample.status, 1000)
	}
	v := s.TrafficStats()
	if v.Requests != 2 || v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 7 || *v.HitRate != 50 {
		t.Fatalf("unexpected statistics: %+v", v)
	}
	s.recordTraffic("GET", "HIT", "completed", 200, 100)
	if s.TrafficStats().SavedBytes != 107 {
		t.Fatal("lost traffic totals")
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
	if v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 4 {
		t.Fatalf("HTTP statistics: %+v", v)
	}
}

func TestLegacyLatencyIsPreservedButNotProduced(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := s.usage.db.Exec(`UPDATE traffic_totals SET local_samples=3,local_ns=4,upstream_samples=5,upstream_ns=6 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	s.recordTraffic("GET", "HIT", "completed", 200, 7)
	var a, b, c, d int
	if err := s.usage.db.QueryRow(`SELECT local_samples,local_ns,upstream_samples,upstream_ns FROM traffic_totals WHERE id=1`).Scan(&a, &b, &c, &d); err != nil {
		t.Fatal(err)
	}
	if a != 3 || b != 4 || c != 5 || d != 6 {
		t.Fatal("historical samples overwritten")
	}
	raw, err := json.Marshal(s.TrafficStats())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"localSamples", "localMS", "upstreamSamples", "upstreamMS", "reductionPercent"} {
		if _, ok := fields[key]; ok {
			t.Fatal("legacy output", key)
		}
	}
}
