package cacheproxy

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"modernc.org/sqlite"
)

var usageImportPauseSequence atomic.Uint64

func TestHistoricalUsageImportAllowsCachedPlayback(t *testing.T) {
	for _, outcome := range []string{"commit", "rollback", "small-cache"} {
		t.Run(outcome, func(t *testing.T) { testImportCachedPlayback(t, outcome) })
	}
}

func testImportCachedPlayback(t *testing.T, outcome string) {
	t.Helper()
	rowCount := 2
	if outcome == "small-cache" {
		rowCount = 4000
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(release) }) }
	defer unblock()
	name := fmt.Sprintf("swd_test_usage_import_%d", usageImportPauseSequence.Add(1))
	var inserted atomic.Int64
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if inserted.Add(1) == int64(rowCount) {
			close(entered)
			<-release
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
	if _, err := s.PrefetchSong(t.Context(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	s.Store.usage.startGET("99", "")
	s.Store.usage.flush(t.Context())
	if _, err := s.Store.db.Exec("CREATE TRIGGER pause_import BEFORE INSERT ON song_usage BEGIN SELECT " + name + "(); END"); err != nil {
		t.Fatal(err)
	}
	if outcome == "small-cache" {
		if _, err := s.Store.db.Exec("PRAGMA cache_size=1"); err != nil {
			t.Fatal(err)
		}
	}
	var spillBefore int
	if err := s.Store.db.QueryRow("PRAGMA cache_spill").Scan(&spillBefore); err != nil {
		t.Fatal(err)
	}
	history := historyAt(42, time.Now().Add(-time.Hour).UnixMilli())
	for id := 0; id < rowCount-1; id++ {
		history.Observe(int64(1000+id), history.LastAt)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Store.ImportSongUsage(ctx, history, true) }()
	finished := false
	defer func() {
		unblock()
		if !finished {
			<-done
		}
	}()
	select {
	case <-entered:
	case err := <-done:
		finished = true
		t.Fatal("import did not pause inside the SQLite write transaction:", err)
	case <-time.After(5 * time.Second):
		t.Fatal("import did not enter the real SQLite write transaction")
	}
	// Readers must see the old committed table, never the partial replacement.
	var oldRows int
	readCtx, stopRead := context.WithTimeout(t.Context(), time.Second)
	defer stopRead()
	if err := s.Store.playbackDB.QueryRowContext(readCtx, "SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&oldRows); err != nil || oldRows != 1 {
		t.Fatal("uncommitted replacement became visible:", oldRows, err)
	}

	server := httptest.NewServer(s)
	defer func() { unblock(); server.Close() }()
	client := &http.Client{Timeout: time.Second}
	for _, tc := range []struct {
		method, host, path, span, body string
		status                         int
	}{
		{http.MethodGet, "api.udon.dance", "/Api/Songs/play?id=42", "", payload, http.StatusOK},
		{http.MethodGet, "api.udon.dance", "/Api/Songs/play?id=42", "bytes=2-7", payload[2:8], http.StatusPartialContent},
		{http.MethodGet, "play.udon.dance", videoURL(payload)[len("http://play.udon.dance"):], "", payload, http.StatusOK},
		{http.MethodHead, "api.udon.dance", "/Api/Songs/play?id=42", "", "", http.StatusOK},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		req.Header.Set("Range", tc.span)
		started := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("cached %s %s waited for paused history import: %v", tc.method, tc.span, err)
		}
		first := make([]byte, min(1, len(tc.body)))
		_, firstErr := io.ReadFull(resp.Body, first)
		t.Logf("%s %s first cached byte while import is paused: %s", tc.method, tc.span, time.Since(started))
		rest, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if firstErr != nil || readErr != nil || resp.StatusCode != tc.status || resp.Header.Get("X-SWD-Cache") != "HIT" || string(append(first, rest...)) != tc.body {
			t.Fatalf("cached response: status=%d cache=%s body=%q errors=%v/%v", resp.StatusCode, resp.Header.Get("X-SWD-Cache"), append(first, rest...), firstErr, readErr)
		}
	}
	if outcome == "rollback" {
		cancel()
	}
	unblock()
	err := <-done
	finished = true
	if (outcome == "rollback" && !errors.Is(err, context.Canceled)) || (outcome != "rollback" && err != nil) {
		t.Fatal("import result:", err)
	}
	s.Store.usage.flush(t.Context())
	var spillAfter int
	if err := s.Store.db.QueryRow("PRAGMA cache_spill").Scan(&spillAfter); err != nil || spillAfter != spillBefore {
		t.Fatal("import did not restore writer cache spilling:", spillBefore, spillAfter, err)
	}
	wantDemand, wantOld := 2, 0
	if outcome == "rollback" {
		wantDemand, wantOld = 1, 1
	}
	var demand, old int
	if err := s.Store.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=42").Scan(&demand); err != nil || demand != wantDemand {
		t.Fatal("GET accepted during import was lost, reordered or counted twice:", demand, err)
	}
	if err := s.Store.db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&old); err != nil || old != wantOld {
		t.Fatal("replacement/rollback did not preserve the expected old table:", old, err)
	}
}

func TestUsageFullQueueDoesNotBlockGETRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := newUsage(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		u.started = true
		// No worker: model a paused import with a full observation queue.
		for range cap(u.events) {
			u.events <- usageEvent{}
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- u.enqueueControl(ctx, usageEvent{barrier: make(chan struct{})}) }()
		synctest.Wait()
		// Both operations must retain the existing nonblocking overflow policy
		// even while a reliable command waits for queue space.
		u.startGET("42", "")
		u.record(usageEvent{songID: "42", method: http.MethodGet})
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("full-queue command did not respect cancellation:", err)
		}
	})
}
