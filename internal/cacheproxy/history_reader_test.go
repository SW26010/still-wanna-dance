package cacheproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"modernc.org/sqlite"
)

func TestHistoryConcurrentRefreshSharesStatistics(t *testing.T) {
	for _, cancelLeader := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelLeader), func(t *testing.T) {
			s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.usage.unlimited = true
			if err := s.usage.write([]usageEvent{{method: "GET", at: 1, sources: &ResponseSources{DownloadBytes: 7}}}); err != nil {
				t.Fatal(err)
			}
			h := s.History()
			snapshot, releaseSnapshot, err := h.snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer releaseSnapshot()
			snapshot.db.SetMaxOpenConns(1)
			synctest.Test(t, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int64
				name := "swd_history_refresh_" + strconv.FormatUint(historyCountSequence.Add(1), 10)
				if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
					if calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					return int64(1), nil
				}); err != nil {
					t.Fatal(err)
				}
				// Cancellation may discard the physical connection. Install the
				// scan probe on every replacement, not just the first connection.
				sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
					if dsn != storageDatabaseURI(snapshot.path, "ro") {
						return nil
					}
					_, err := conn.ExecContext(context.Background(), "CREATE TEMP VIEW request_events AS SELECT * FROM main.request_events WHERE "+name+"()", nil)
					return err
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				leader := make(chan error, 1)
				go func() { _, err := h.Sources(ctx); leader <- err }()
				<-entered
				waiters := make(chan error, 8)
				for i := 0; i < 8; i++ {
					go func() {
						got, err := h.Sources(t.Context())
						if err == nil && got.DownloadBytes != 7 {
							err = errors.New("wrong cached sources")
						}
						waiters <- err
					}()
				}
				synctest.Wait()
				if cancelLeader {
					cancel()
				}
				close(release)
				if err := <-leader; cancelLeader && !errors.Is(err, context.Canceled) || !cancelLeader && err != nil {
					t.Errorf("leader: %v", err)
				}
				for i := 0; i < 8; i++ {
					if err := <-waiters; err != nil {
						t.Error("waiter:", err)
					}
				}
				want := int64(1)
				if cancelLeader {
					want++
				}
				if calls.Load() != want {
					t.Errorf("statistics scans=%d want=%d", calls.Load(), want)
				}
			})
		})
	}
}

func TestHistoryRefreshReusesUnchangedDetails(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	ready := false
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET", songID: "42", status: 200, bytes: 1, firstBodyNS: 123, firstBodyBlocking: &ready, sources: &ResponseSources{DownloadBytes: 7}}}); err != nil {
		t.Fatal(err)
	}
	h := s.History()
	before, err := h.Sources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	old := h.current
	var indexes int
	if err := old.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='request_events_history_song_time'").Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Error("source refresh prepared unused song index")
	}
	latency, err := h.Latency(t.Context(), time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	// Remove the rate-limit wait from this deterministic rebuild assertion.
	h.nextCopy = time.Time{}
	after, err := h.Sources(t.Context())
	if err != nil || before != after {
		t.Fatalf("sources: %+v %v", after, err)
	}
	if h.current != old {
		t.Error("unrelated preference commit rebuilt entire history")
	}
	// The real private connection rejects scans after the first success. Cache
	// reuse must survive, including a moving window with no boundary crossings.
	h.current.db.SetMaxOpenConns(1)
	if _, err := h.current.db.Exec("CREATE TEMP VIEW request_events AS SELECT * FROM missing_history_table"); err != nil {
		t.Fatal(err)
	}
	if got, err := h.Sources(t.Context()); err != nil || got != before {
		t.Errorf("sources rescanned unchanged history: %+v %v", got, err)
	}
	got, err := h.Latency(t.Context(), time.UnixMilli(32000))
	latency.From = time.UnixMilli(32000).Add(-72 * time.Hour).UnixMilli()
	latency.To = 32000
	if err != nil || !reflect.DeepEqual(got, latency) {
		t.Errorf("latency rescanned unchanged window: %+v %v", got, err)
	}
}

func TestHistoryWaitersRecoverCanceledBuild(t *testing.T) {
	for _, outcome := range []string{"cancel", "deadline", "waiter-cancel", "error", "close"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
				t.Setenv(key, root)
			}
			synctest.Test(t, func(t *testing.T) {
				s, err := createEmptyFixtureStore(storagePath(root))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				defer unblock()
				var copies atomic.Int64
				var paths sync.Map
				copyError := errors.New("controlled history copy failure")
				sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
					uri, _ := url.Parse(dsn)
					if uri == nil || uri.Query().Get("mode") != "rw" || !strings.Contains(uri.Path, filepath.ToSlash(root)+"/swd-storage-snapshot-") {
						return nil
					}
					if _, seen := paths.LoadOrStore(uri.Path, true); seen {
						return nil
					}
					if copies.Add(1) == 1 {
						close(entered)
						<-release
						if outcome == "error" {
							return copyError
						}
					}
					return nil
				})
				leaderCtx, cancel := context.WithCancel(t.Context())
				if outcome == "deadline" {
					cancel()
					leaderCtx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				sourceCtx, cancelSource := context.WithCancel(t.Context())
				defer cancelSource()
				leader, sources, latency := make(chan error, 1), make(chan error, 1), make(chan error, 1)
				go func() { _, err := s.History().Export(leaderCtx, 1, 3000, io.Discard); leader <- err }()
				<-entered
				go func() { _, err := s.History().Sources(sourceCtx); sources <- err }()
				go func() { _, err := s.History().Latency(t.Context(), time.Now()); latency <- err }()
				// Wait until both live callers have joined the paused shared build.
				// Fake time also exercises the unchanged copy interval without sleep.
				synctest.Wait()
				for _, result := range []chan error{sources, latency} {
					select {
					case err := <-result:
						t.Fatalf("waiter returned before build settled: %v", err)
					default:
					}
				}
				switch outcome {
				case "cancel":
					cancel()
				case "deadline":
					<-leaderCtx.Done()
				case "waiter-cancel":
					cancelSource()
				case "close":
					s.History().Close()
				}
				unblock()
				leaderErr := <-leader
				if outcome == "cancel" && !errors.Is(leaderErr, context.Canceled) {
					t.Errorf("leader cancellation: %v", leaderErr)
				}
				if outcome == "deadline" && !errors.Is(leaderErr, context.DeadlineExceeded) {
					t.Errorf("leader deadline: %v", leaderErr)
				}
				if outcome == "error" && !errors.Is(leaderErr, copyError) {
					t.Errorf("leader copy failure: %v", leaderErr)
				}
				if outcome == "waiter-cancel" && leaderErr != nil {
					t.Errorf("waiter canceled the leader: %v", leaderErr)
				}
				for i, result := range []chan error{sources, latency} {
					err := <-result
					switch outcome {
					case "cancel", "deadline":
						if err != nil {
							t.Errorf("valid waiter %d inherited canceled build: %v", i, err)
						}
					case "error":
						if !errors.Is(err, copyError) {
							t.Errorf("waiter lost real copy failure: %v", err)
						}
					case "waiter-cancel":
						if i == 0 && !errors.Is(err, context.Canceled) || i == 1 && err != nil {
							t.Errorf("independent waiter %d cancellation: %v", i, err)
						}
					case "close":
						if err == nil {
							t.Error("closed history accepted a waiting query")
						}
					}
				}
				want := int64(1)
				if outcome == "cancel" || outcome == "deadline" {
					want = 2
				}
				if copies.Load() != want {
					t.Errorf("copies=%d, want %d", copies.Load(), want)
				}
			})
		})
	}
}

func TestHistorySharesCopiesAndBoundsRefresh(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, root)
	}
	s, err := createEmptyFixtureStore(storagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history := NewRequestHistory(s.path)
	defer history.Close()
	var copies atomic.Int64
	var paths sync.Map
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		uri, _ := url.Parse(dsn)
		if uri != nil && uri.Query().Get("mode") == "rw" && strings.Contains(uri.Path, filepath.ToSlash(root)+"/swd-storage-snapshot-") {
			if _, seen := paths.LoadOrStore(uri.Path, true); !seen {
				copies.Add(1)
			}
		}
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := history.Export(t.Context(), 1, 3000, io.Discard); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, operation := range []string{"sources", "latency", "time", "export"} {
		if _, err := readHistoryOperation(t.Context(), history, operation); err != nil {
			t.Fatal(operation, err)
		}
	}
	if copies.Load() != 1 {
		t.Fatalf("same committed database made %d copies", copies.Load())
	}
	s.usage.unlimited = true
	if err := s.usage.write([]usageEvent{{at: 1000, method: "GET"}}); err != nil {
		t.Fatal(err)
	}
	// A new commit must not return an old count. Cancellation during the
	// minimum copy interval stops waiting without creating another copy.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := history.Export(ctx, 1, 3000, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh wait: %v", err)
	}
	if copies.Load() != 1 {
		t.Fatalf("refresh bypassed copy interval: %d", copies.Load())
	}
	count, err := history.Export(t.Context(), 1, 3000, io.Discard)
	if err != nil || count != 1 || copies.Load() != 2 {
		t.Fatalf("fresh snapshot: %+v copies=%d %v", count, copies.Load(), err)
	}
	// data_version belongs to a physical connection. Losing that connection
	// must discard the cached version before the new counter starts at one.
	if err := s.usage.write([]usageEvent{{at: 2000, method: "GET"}}); err != nil {
		t.Fatal(err)
	}
	if err := history.versionConn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := history.Export(t.Context(), 1, 3000, io.Discard); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("monitor failure: %v", err)
	}
	count, err = history.Export(t.Context(), 1, 3000, io.Discard)
	if err != nil || count != 2 || copies.Load() != 3 {
		t.Fatalf("reconnected monitor served stale copy: %+v copies=%d %v", count, copies.Load(), err)
	}
	history.Close()
	if _, err := history.Page(t.Context(), RequestQuery{Limit: 1}); err == nil {
		t.Fatal("closed history accepted a query")
	}
}
