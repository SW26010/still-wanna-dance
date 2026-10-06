package cacheproxy

import (
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlaybackTransfersExcludePrefetchFailuresAndDeduplicateRanges(t *testing.T) {
	s, cfg := setup(t, nil)
	e := usageEvent{songID: "42", source: "http", method: "GET", status: 206, outcome: "completed", bytes: 10, cache: "HIT", firstBodyNS: int64(20 * time.Millisecond), at: time.Now().UnixMilli()}
	s.usage.record(e)
	e.at++
	s.usage.record(e)
	for _, bad := range []usageEvent{
		{songID: "42", source: "prefetch", method: "GET", status: 200, outcome: "completed", bytes: 10},
		{songID: "42", source: "http", method: "HEAD", status: 200, outcome: "completed", bytes: 10},
		{songID: "42", source: "http", method: "GET", status: 502, outcome: "failed", bytes: 10},
		{songID: "42", source: "http", method: "GET", status: 206, outcome: "canceled", bytes: 10},
		{songID: "42", source: "http", method: "GET", status: 200, outcome: "completed"},
	} {
		bad.at = e.at + 60000
		s.usage.record(bad)
	}
	e.at += 30000
	e.cache = "MISS"
	e.firstBodyNS = int64(100 * time.Millisecond)
	s.usage.record(e)
	s.usage.flush()
	v := s.TrafficStats()
	if v.PlaybackTransfers != 2 || v.LocalBodySamples != 2 || v.ColdBodySamples != 1 || v.LocalBodyMS == nil || *v.LocalBodyMS != 20 || v.ColdBodyMS == nil || *v.ColdBodyMS != 100 {
		t.Fatalf("%+v", v)
	}
	var count int
	if err := s.usage.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=42").Scan(&count); err != sql.ErrNoRows {
		t.Fatal("transfer changed retention demand", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if saved := ReadTrafficStats(cfg.StorageDir); saved.PlaybackTransfers != 2 || saved.LocalBodyMS == nil || *saved.LocalBodyMS != 20 {
		t.Fatal(saved)
	}
}

func TestFirstBodyTimingIsSeparateFromHeaders(t *testing.T) {
	w := &responseWriter{ResponseWriter: httptest.NewRecorder(), started: time.Now().Add(-time.Second)}
	w.WriteHeader(200)
	if w.firstBodyMS() != nil {
		t.Fatal("headers counted as body")
	}
	// Set a known earlier header boundary without depending on clock precision.
	w.headerLatency = 10 * time.Millisecond
	if _, err := w.Write([]byte("video")); err != nil {
		t.Fatal(err)
	}
	if w.firstBodyLatency < time.Second || w.firstBodyLatency <= w.headerLatency {
		t.Fatal(w.firstBodyLatency, w.headerLatency)
	}
	first := w.firstBodyLatency
	w.Write([]byte("more"))
	if w.firstBodyLatency != first {
		t.Fatal("later write changed first-body time")
	}
}
