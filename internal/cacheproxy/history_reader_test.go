package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"modernc.org/sqlite"
)

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
				copyError := errors.New("controlled history copy failure")
				sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
					uri, _ := url.Parse(dsn)
					if uri == nil || uri.Query().Get("mode") != "rw" || !strings.Contains(uri.Path, filepath.ToSlash(root)+"/swd-storage-snapshot-") {
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
				go func() { _, err := s.History().Count(leaderCtx, RequestQuery{Limit: 1}); leader <- err }()
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
	var copies atomic.Int64
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		uri, _ := url.Parse(dsn)
		if uri != nil && uri.Query().Get("mode") == "rw" && strings.Contains(uri.Path, filepath.ToSlash(root)+"/swd-storage-snapshot-") {
			copies.Add(1)
		}
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.History().Count(t.Context(), RequestQuery{Limit: 1}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, operation := range []string{"sources", "latency", "song", "resource", "export"} {
		if _, err := readHistoryOperation(t.Context(), s.History(), operation); err != nil {
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
	if _, err := s.History().Count(ctx, RequestQuery{Limit: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh wait: %v", err)
	}
	if copies.Load() != 1 {
		t.Fatalf("refresh bypassed copy interval: %d", copies.Load())
	}
	count, err := s.History().Count(t.Context(), RequestQuery{Limit: 1})
	if err != nil || count.Total != 1 || copies.Load() != 2 {
		t.Fatalf("fresh snapshot: %+v copies=%d %v", count, copies.Load(), err)
	}
	// data_version belongs to a physical connection. Losing that connection
	// must discard the cached version before the new counter starts at one.
	if err := s.usage.write([]usageEvent{{at: 2000, method: "GET"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.History().versionConn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.History().Count(t.Context(), RequestQuery{Limit: 1}); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("monitor failure: %v", err)
	}
	count, err = s.History().Count(t.Context(), RequestQuery{Limit: 1})
	if err != nil || count.Total != 2 || copies.Load() != 3 {
		t.Fatalf("reconnected monitor served stale copy: %+v copies=%d %v", count, copies.Load(), err)
	}
	s.History().Close()
	if _, err := s.History().Page(t.Context(), RequestQuery{Limit: 1}); err == nil {
		t.Fatal("closed history accepted a query")
	}
}
