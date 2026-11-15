package cacheproxy

import (
	"context"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestHistoryPreparesIndexesDuringLeasedExport(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET", songID: "42"}, {at: 1100, method: "HEAD"}}); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	h := s.History()
	w := &exportPauseWriter{Writer: io.Discard, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := h.Export(t.Context(), 1, 2000, w); done <- err }()
	select {
	case <-w.entered:
	case err := <-done:
		t.Fatalf("export: %v", err)
	}
	defer func() {
		close(w.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	current := h.current
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := h.Page(ctx, RequestQuery{Limit: 1, Sort: "time"}); results <- err }()
	go func() { _, err := h.Latency(ctx, time.UnixMilli(2000)); results <- err }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error("on-demand index blocked by export:", err)
		}
	}
	if h.current != current {
		t.Error("index preparation replaced leased copy")
	}
	var after string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&after); err != nil || after != mode {
		t.Fatalf("business journal changed: %s %s %v", mode, after, err)
	}
	var derived int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'request_events_history_%'").Scan(&derived); err != nil || derived != 0 {
		t.Fatalf("business schema changed: %d %v", derived, err)
	}
}

func TestHistoryOwnedInvalidationAndCurrentNames(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, h := s.usage, s.History()
	u.unlimited = true
	get := usageEvent{method: "GET", at: 1000, songID: "42", sources: &ResponseSources{DownloadBytes: 1}}
	if err := u.write([]usageEvent{get}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO songs(id,name) VALUES(42,'old')"); err != nil {
		t.Fatal(err)
	}
	q := RequestQuery{Limit: 1, Sort: "time"}
	page, err := h.Page(t.Context(), q)
	if err != nil || page.Requests[0].SongTitle != "old" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if _, err := h.Sources(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := h.current
	if _, err := s.db.Exec("UPDATE songs SET name='new' WHERE id=42"); err != nil {
		t.Fatal(err)
	}
	if err := u.write([]usageEvent{{summaryOnly: true, songID: "42", at: 2000}}); err != nil {
		t.Fatal(err)
	}
	if err := u.write([]usageEvent{get, {songID: "invalid"}}); err == nil {
		t.Fatal("expected rollback")
	}
	h.nextCopy = time.Time{}
	page, err = h.Page(t.Context(), q)
	if err != nil || page.Requests[0].SongTitle != "new" || h.current != before {
		t.Fatalf("unrelated writes rebuilt history or left stale title: %+v %v", page, err)
	}
	for _, method := range []string{"HEAD", "GET"} {
		e := get
		e.method = method
		if err := u.write([]usageEvent{e}); err != nil {
			t.Fatal(err)
		}
		h.nextCopy = time.Time{}
		if _, err := h.Sources(t.Context()); err != nil {
			t.Fatal(err)
		}
		if h.current == before {
			t.Fatal("detail commit did not invalidate", method)
		}
		before = h.current
	}
	if n, err := h.Export(t.Context(), 1, 2000, io.Discard); err != nil || n != 3 {
		t.Fatalf("export lost HEAD: %d %v", n, err)
	}
	u.unlimited = false
	if n, err := u.prune(time.UnixMilli(2000)); err != nil || n != 3 {
		t.Fatalf("prune: %d %v", n, err)
	}
	h.nextCopy = time.Time{}
	if got, err := h.Sources(t.Context()); err != nil || got != (ResponseSources{}) || h.current == before {
		t.Fatalf("pruned sources: %+v %v", got, err)
	}
}

func TestHistoryLatencyCacheTracksExactWindow(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	now := time.UnixMilli(300000000)
	var events []usageEvent
	for i := 0; i < 103; i++ {
		blocking := i%3 == 0
		// Duplicate values, both groups, odd/even sizes, expired and future
		// samples, with exact lower-inclusive and upper-exclusive boundaries.
		at := []int64{now.Add(-72*time.Hour).UnixMilli() - 1, now.Add(-72 * time.Hour).UnixMilli(), now.UnixMilli() - 1, now.UnixMilli(), now.UnixMilli() + 30000}[i%5]
		events = append(events, usageEvent{method: "GET", at: at, status: 206, bytes: 1, firstBodyNS: int64(i%11) * 1000001, firstBodyBlocking: &blocking})
	}
	if err := s.usage.write(events); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []time.Duration{0, time.Millisecond, 30 * time.Second, 30*time.Second + time.Millisecond, -time.Millisecond, 0, 73 * time.Hour} {
		at := now.Add(delta)
		got, err := s.History().Latency(t.Context(), at)
		if err != nil {
			t.Fatal(err)
		}
		want := FirstBodyLatency{From: at.Add(-72 * time.Hour).UnixMilli(), To: at.UnixMilli()}
		for i, group := range []*LatencyGroup{&want.Ready, &want.Blocking} {
			var ns []int64
			for _, e := range events {
				if *e.firstBodyBlocking == (i == 1) && e.at >= want.From && e.at < want.To {
					ns = append(ns, e.firstBodyNS)
				}
			}
			slices.Sort(ns)
			group.Samples = int64(len(ns))
			if n := len(ns); n > 0 {
				p50, p95 := float64(ns[(n+1)/2-1])/1e6, float64(ns[(95*n+99)/100-1])/1e6
				group.P50MS, group.P95MS = &p50, &p95
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("delta=%s got=%+v want=%+v", delta, got, want)
		}
		// Caller mutations must not poison later cached results.
		if got.Ready.P50MS != nil {
			*got.Ready.P50MS = -1
		}
		again, err := s.History().Latency(t.Context(), at)
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("mutable cached quantile: %+v %v", again, err)
		}
	}
}
