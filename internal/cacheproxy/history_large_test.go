package cacheproxy

import (
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in scale evidence with production read/write timeouts and actual collector.
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
	now := time.Now()
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
 INSERT INTO request_events(event_id,requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,resource_md5,song_id,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes)
 SELECT i,?+i/3,'GET','',CASE WHEN i%2=0 THEN 'completed' ELSE 'canceled' END,1,1,206,printf('%032x',i%997+1),i%1001+1,i%1000000,0,1,2,3 FROM n`, size, now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cold, err := s.History().Statistics(t.Context(), now)
	t.Logf("cold bounded statistics: %s; raw=%d samples=%d", time.Since(start), cold.Requests, cold.Ready.Samples)
	if err != nil || cold.Requests != 10000 || cold.Ready.Samples != 10000 || cold.Sources.DownloadBytes != 10000 || cold.Ready.P50MS == nil || *cold.Ready.P50MS != 0.994999 || *cold.Ready.P95MS != 0.999499 {
		t.Fatalf("dense window: %+v %v", cold, err)
	}
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+historyStatisticsSQL, cold.From, cold.To, historyStatisticsLimit)
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
	if !strings.Contains(plan.String(), "request_events_time") || strings.Contains(plan.String(), "TEMP B-TREE") {
		t.Fatal("unbounded statistics plan", plan.String())
	}
	t.Log(plan.String())
	s.usage.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := s.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	write := func(id int) time.Duration {
		start := time.Now()
		song := strconv.Itoa(50000 + id)
		at := s.usage.startGET(song, "")
		s.usage.record(usageEvent{at: at, songID: song, method: "GET", outcome: "completed"})
		s.usage.flush(t.Context())
		return time.Since(start)
	}
	var baseline []time.Duration
	for i := 0; i < 16; i++ {
		baseline = append(baseline, write(i))
	}
	stop, done := make(chan struct{}), make(chan []time.Duration, 1)
	go func() {
		var times []time.Duration
		for i := 16; ; i++ {
			select {
			case <-stop:
				done <- times
				return
			default:
				times = append(times, write(i))
			}
		}
	}()
	start = time.Now()
	for i := 0; i < 12; i++ {
		q := RequestQuery{Limit: 50, HideAdjacentCanceled: true}
		if i%2 == 1 {
			q.Direction = "ascending"
		}
		first, err := s.History().Page(t.Context(), q)
		if err != nil || len(first.Requests) != 50 || !first.HasMore {
			t.Errorf("page: %v", err)
			break
		}
		q.Cursor = first.NextCursor
		next, err := s.History().Page(t.Context(), q)
		if err != nil || len(next.Requests) != 50 || next.Requests[0].ID == first.Requests[49].ID {
			t.Errorf("next page: %v", err)
			break
		}
		if _, err := readRequestStatistics(t.Context(), s.path, time.Now()); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	loaded := <-done
	if err := s.usage.close(); err != nil {
		t.Fatal("collector writes", err)
	}
	if len(loaded) == 0 {
		t.Fatal("no concurrent writes")
	}
	slices.Sort(baseline)
	slices.Sort(loaded)
	t.Logf("12 page-pairs + forced statistics: %s; commits=%d baseline p50/p95=%s/%s concurrent p50/p95/max=%s/%s/%s", time.Since(start), len(loaded), baseline[7], baseline[15], loaded[(len(loaded)-1)/2], loaded[(95*len(loaded)+99)/100-1], loaded[len(loaded)-1])
	if s.History().current != nil || s.History().building != nil {
		t.Fatal("daily query created an export snapshot")
	}
	var derived int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'request_events_history_%'").Scan(&derived); err != nil || derived != 0 {
		t.Fatalf("derived indexes %d %v", derived, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, query := range []string{"SELECT count(*) FROM request_events WHERE song_id>=50000", "SELECT count(*) FROM song_usage WHERE demand_count=1 AND demand_score=1"} {
		var got int
		if err := reopened.db.QueryRow(query).Scan(&got); err != nil || got != 16+len(loaded) {
			t.Errorf("persisted %d want %d: %v", got, 16+len(loaded), err)
		}
	}
}
