package cacheproxy

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var historyCountSequence atomic.Uint64

func TestHistoryReadsAllowPersistentBusinessWrites(t *testing.T) {
	for _, operation := range []string{"sources", "latency", "song", "resource", "export"} {
		for _, outcome := range []string{"complete", "scan-error"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				testHistoryCountBusinessWrites(t, operation, outcome == "scan-error")
			})
		}
	}

}

func testHistoryCountBusinessWrites(t *testing.T, operation string, failScan bool) {
	t.Helper()
	root := t.TempDir()
	// Keep both the business database and any private snapshot in this test's
	// scope. The hook below never changes another test's connections or schema.
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, root)
	}
	s, err := createEmptyFixtureStore(filepath.Join(root, "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	ready := false
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET", songID: "42", key: strings.Repeat("a", 32), status: 200, bytes: 1, firstBodyNS: 1, firstBodyBlocking: &ready, sources: &ResponseSources{DownloadBytes: 1}, outcome: "completed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	var pause, resume sync.Once
	unblock := func() { resume.Do(func() { close(release) }) }
	defer unblock()
	name := "swd_test_history_count_" + strconv.FormatUint(historyCountSequence.Add(1), 10)
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		pause.Do(func() { close(entered); <-release })
		if failScan {
			return nil, errors.New("controlled count scan failure")
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		uri, err := url.Parse(dsn)
		if err != nil || uri.Query().Get("mode") != "ro" || !strings.Contains(uri.Path, filepath.ToSlash(root)+"/") {
			return nil
		}
		// A TEMP view leaves the validated on-disk schema intact. Evaluating
		// the actual count pauses only after SQLite has acquired its read lock,
		// on either the business file (regression) or the private snapshot.
		_, err = conn.ExecContext(context.Background(), "CREATE TEMP VIEW request_events AS SELECT * FROM main.request_events WHERE "+name+"()", nil)
		return err
	})

	type countResult struct {
		value any
		err   error
	}
	done := make(chan countResult, 1)
	go func() {
		value, err := readHistoryOperation(t.Context(), s.History(), operation)
		done <- countResult{value, err}
	}()
	finished := false
	defer func() {
		unblock()
		if !finished {
			<-done
		}
	}()
	select {
	case <-entered:
	case result := <-done:
		finished = true
		t.Fatalf("count did not enter SQLite scan: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("count did not enter SQLite scan")
	}

	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Errorf("favorite during count: %v", err)
	}
	// Exercise the actual non-retrying collector, including both detail rows
	// and demand updates, rather than calling its write method directly.
	if err := s.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	at := s.usage.startGET("42", "")
	s.usage.record(usageEvent{songID: "42", at: at, method: "GET", outcome: "completed"})
	s.usage.flush(t.Context())

	checkWrites := func(store *Store) {
		t.Helper()
		policies, err := store.SongPolicies(t.Context())
		if err != nil || policies[42] != SongFavorite {
			t.Errorf("persisted favorite: %v, %v", policies, err)
		}
		var rows, demands int
		var score float64
		if err := store.db.QueryRow("SELECT count(*) FROM request_events WHERE requested_at=? AND song_id=42", at).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("persisted request batch: rows=%d, %v", rows, err)
		}
		if err := store.db.QueryRow("SELECT demand_count,demand_score FROM song_usage WHERE song_id=42").Scan(&demands, &score); err != nil || demands != 1 || score != 1 {
			t.Errorf("persisted demand: count=%d score=%g, %v", demands, score, err)
		}
	}
	checkWrites(s)
	if err := s.Close(); err != nil {
		t.Errorf("collector failure: %v", err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	checkWrites(reopened)

	unblock()
	result := <-done
	finished = true
	if failScan {
		if result.err == nil || !strings.Contains(result.err.Error(), "controlled count scan failure") {
			t.Errorf("count scan failure: %v", result.err)
		}
	} else {
		if result.err != nil {
			t.Errorf("isolated read: %v", result.err)
		}
		switch value := result.value.(type) {
		case ResponseSources:
			if value.DownloadBytes != 1 {
				t.Errorf("snapshot sources: %+v", value)
			}
		case FirstBodyLatency:
			if value.Ready.Samples != 1 {
				t.Errorf("snapshot latency: %+v", value)
			}
		case RecentRequests:
			if len(value.Requests) != 1 || value.Requests[0].At != 1000 {
				t.Errorf("snapshot page: %+v", value)
			}
		case int64:
			if value != 1 {
				t.Errorf("snapshot export: %d", value)
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Errorf("count left temporary snapshot directory: %s", entry.Name())
		}
	}
}

// These are the same typed operations used by Console; all share one reader.
func readHistoryOperation(ctx context.Context, history *RequestHistory, operation string) (any, error) {
	switch operation {
	case "sources":
		return history.Sources(ctx)
	case "latency":
		return history.Latency(ctx, time.UnixMilli(2000))
	case "export":
		return history.Export(ctx, 1, 2000, io.Discard)
	default:
		return history.Page(ctx, RequestQuery{Limit: 1, Sort: operation})
	}
}
