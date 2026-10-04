package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestExponentialDemandSurvivesRestartAndCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u, err := openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-120 * 24 * time.Hour)
	recordDemand(u, "song", base)
	recordDemand(u, "song", base.Add(29*time.Second)) // Same dedup window.
	u.close()
	u, err = openUsage(path, log, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	recordDemand(u, "song", base.Add(60*24*time.Hour))
	// A backward clock must not increase heat or move the accumulator timestamp.
	recordDemand(u, "song", base.Add(59*24*time.Hour))
	u.flush()
	cleaner := &usageStore{db: u.db, retention: 30 * 24 * time.Hour}
	if n, err := cleaner.prune(time.Now()); err != nil || n != 4 {
		t.Fatalf("prune %d: %v", n, err)
	}
	var score float64
	var last, count int64
	if err := u.db.QueryRow(`SELECT demand_score,last_demand_at,demand_count FROM resource_usage WHERE resource_key='song'`).Scan(&score, &last, &count); err != nil {
		t.Fatal(err)
	}
	if score != 1.5 || count != 2 || last != base.Add(60*24*time.Hour).UnixMilli() {
		t.Fatalf("score=%g count=%d last=%d", score, count, last)
	}
	if got := retentionScore(score, last, base.Add(120*24*time.Hour).UnixMilli()); got != .75 {
		t.Fatalf("score after two half-lives: %g", got)
	}
	// New demand adds one, rather than rejuvenating the cumulative count.
	if err := u.write([]usageEvent{{id: "song", at: base.Add(120 * 24 * time.Hour).UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	if err := u.db.QueryRow(`SELECT demand_score FROM resource_usage WHERE resource_key='song'`).Scan(&score); err != nil {
		t.Fatal(err)
	}
	if score != 1.75 {
		t.Fatalf("new demand revived old history: %g", score)
	}
}

func TestExponentialPriorityCombinesDifferentVersionAges(t *testing.T) {
	s, _ := setup(t, nil)
	seedPrioritySong(t, s, "1981", "old")
	seedPrioritySong(t, s, "1981", "new")
	now := time.Now().UnixMilli()
	if err := s.usage.write([]usageEvent{
		{id: "song:1981", at: now - (120 * 24 * time.Hour).Milliseconds(), summaryOnly: true},
		{id: "song:1981", at: now, summaryOnly: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []time.Duration{0, 60 * 24 * time.Hour} {
		scores, err := s.songPriorities(context.Background(), []int64{1981}, now+delta.Milliseconds())
		want := 1.25
		if delta != 0 {
			want = .625
		}
		if err != nil || math.Abs(scores[1981]-want) > 1e-12 {
			t.Fatalf("scores=%v want=%g err=%v", scores, want, err)
		}
	}
}
