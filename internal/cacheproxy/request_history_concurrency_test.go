package cacheproxy

import (
	"context"
	"database/sql/driver"
	"errors"
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

func TestHistoryCountAllowsPersistentBusinessWrites(t *testing.T) {
	for _, outcome := range []string{"complete", "scan-error"} {
		t.Run(outcome, func(t *testing.T) {
			testHistoryCountBusinessWrites(t, outcome == "scan-error")
		})
	}
}

func testHistoryCountBusinessWrites(t *testing.T, failScan bool) {
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
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET", outcome: "completed"}}); err != nil {
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
		count RequestHistoryCount
		err   error
	}
	done := make(chan countResult, 1)
	go func() {
		count, err := CountRequestHistory(t.Context(), s.path, RequestQuery{Limit: 1})
		done <- countResult{count, err}
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
	s.usage.flush()

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
	} else if result.err != nil || result.count.Total != 1 || result.count.StorageID != DatabaseID(s.path) {
		t.Errorf("count changed with later writes: %+v, %v", result.count, result.err)
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
