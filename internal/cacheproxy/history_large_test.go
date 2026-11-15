package cacheproxy

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in disk/scale check with the production busy/transaction timeouts. The
// small deterministic lock/scan tests remain the normal regression gate.
func TestHistoryLargeReadsPersistConcurrentObservations(t *testing.T) {
	if os.Getenv("SWD_HISTORY_LARGE_EVIDENCE") != "1" {
		t.Skip("set SWD_HISTORY_LARGE_EVIDENCE=1 for six million retained rows")
	}
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const size = 6000000
	at := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO request_events(event_id,requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,resource_md5,song_id,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes)
SELECT i,?+i/3,'GET','',CASE WHEN i%2=0 THEN 'completed' ELSE 'canceled' END,1,1,206,
CASE WHEN i%11=0 THEN NULL ELSE printf('%032x',i%997+1) END,
CASE WHEN i%9=0 THEN NULL ELSE i%1001+1 END,i%1000000,0,1,2,3 FROM n`, size, at); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := s.History().Sources(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("cold sources including copy, without song index: elapsed=%s", time.Since(started))
	var derived int
	if err := s.History().current.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'request_events_history_%'").Scan(&derived); err != nil || derived != 0 {
		t.Fatalf("cold statistics prepared unrelated indexes: %d %v", derived, err)
	}
	s.usage.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := s.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	stop, written := make(chan struct{}), make(chan int, 1)
	go func() {
		n := 0
		for n < 32 {
			select {
			case <-stop:
				written <- n
				return
			default:
			}
			id := strconv.Itoa(50000 + n)
			at := s.usage.startGET(id, "")
			s.usage.record(usageEvent{at: at, songID: id, method: "GET", outcome: "completed"})
			s.usage.flush(t.Context())
			n++
			time.Sleep(10 * time.Millisecond)
		}
		written <- n
	}()
	started = time.Now()
	page, readErr := s.History().Page(t.Context(), RequestQuery{Limit: 50, Sort: "song", Direction: "ascending", HideAdjacentCanceled: true})
	t.Logf("cold song page with concurrent collector: elapsed=%s rows=%d err=%v", time.Since(started), len(page.Requests), readErr)
	close(stop)
	n := <-written
	if readErr != nil || len(page.Requests) != 50 {
		t.Errorf("cold sorted page: %v", readErr)
	}
	if err := s.usage.close(); err != nil {
		t.Errorf("collector failure: %v", err)
	}
	for _, sort := range []string{"song", "resource"} {
		for _, direction := range []string{"ascending", "descending"} {
			q := RequestQuery{Limit: 50, Sort: sort, Direction: direction, HideAdjacentCanceled: true}
			started = time.Now()
			first, err := s.History().Page(t.Context(), q)
			t.Logf("page sort=%s direction=%s elapsed=%s rows=%d err=%v", sort, direction, time.Since(started), len(first.Requests), err)
			if err != nil || len(first.Requests) != 50 || !first.HasMore {
				t.Fatalf("sorted page: %+v %v", first, err)
			}
			q.Cursor = first.NextCursor
			started = time.Now()
			next, err := s.History().Page(t.Context(), q)
			t.Logf("next page sort=%s direction=%s elapsed=%s rows=%d err=%v", sort, direction, time.Since(started), len(next.Requests), err)
			if err != nil || len(next.Requests) != 50 || next.Requests[0].ID == first.Requests[49].ID {
				t.Fatalf("next sorted page: %+v %v", next, err)
			}
		}
	}
	for _, operation := range []string{"sources", "latency", "count"} {
		started = time.Now()
		var err error
		switch operation {
		case "sources":
			_, err = s.History().Sources(t.Context())
		case "latency":
			_, err = s.History().Latency(t.Context(), time.Now())
		case "count":
			_, err = s.History().Count(t.Context(), RequestQuery{Limit: 50, HideAdjacentCanceled: true})
		}
		t.Logf("aggregate=%s elapsed=%s err=%v", operation, time.Since(started), err)
		if err != nil {
			t.Errorf("aggregate: %v", err)
		}
	}
	current := s.History().current
	now := time.Now()
	started = time.Now()
	latency, err := readHistoryLatency(t.Context(), current.db, now)
	t.Logf("latency SQL with prepared index: elapsed=%s samples=%d err=%v", time.Since(started), latency.value.Ready.Samples, err)
	if err != nil || latency.value.Ready.Samples != size || latency.value.Ready.P50MS == nil || *latency.value.Ready.P50MS != 0.499999 || *latency.value.Ready.P95MS != 0.949999 {
		t.Fatalf("dense-window quantiles: %+v %v", latency.value, err)
	}
	for i := 1; i <= 3; i++ {
		if err := s.SetSongPolicy(t.Context(), int64(i), SongFavorite); err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		sources, sourceErr := s.History().Sources(t.Context())
		latency, latencyErr := s.History().Latency(t.Context(), now.Add(time.Duration(i)*30*time.Second))
		count, countErr := s.History().Count(t.Context(), RequestQuery{Limit: 50, HideAdjacentCanceled: true})
		t.Logf("refresh=%d after unrelated commit: elapsed=%s same_copy=%v sources=%d samples=%d count=%d errors=%v/%v/%v", i, time.Since(started), s.History().current == current, sources.DownloadBytes, latency.Ready.Samples, count.Total, sourceErr, latencyErr, countErr)
		if sourceErr != nil || latencyErr != nil || countErr != nil || s.History().current != current || sources.DownloadBytes != size || latency.Ready.Samples != size {
			t.Fatal("refresh rebuilt/rescanned or changed aggregates")
		}
	}
	// Explain the exact windows used by Page. The outer sort is bounded by
	// the two limited identity groups, rather than the full request table.
	snapshot, release, err := s.History().snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, sort := range []string{"song", "resource"} {
		query, args := (RequestQuery{Limit: 50, Sort: sort, HideAdjacentCanceled: true}).window(true)
		rows, err := snapshot.db.Query("EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		t.Logf("plan sort=%s:\n%s", sort, plan.String())
	}
	release()
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for query, want := range map[string]int{"SELECT count(*) FROM request_events WHERE song_id>=50000": n, "SELECT count(*) FROM song_usage WHERE demand_count=1 AND demand_score=1": n} {
		var count int
		if err := reopened.db.QueryRow(query).Scan(&count); err != nil || count != want {
			t.Errorf("after reopen: %s = %d, want %d: %v", query, count, want, err)
		}
	}
	t.Logf("after reopen: new details=%d demand rows=%d; production timeouts unchanged", n, n)
}
