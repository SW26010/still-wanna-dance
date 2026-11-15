package cacheproxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Occupy the real application connection until the collector is waiting.
// No collector locks or worker code are substituted.
func blockedUsageStore(t *testing.T) (*Store, func()) {
	t.Helper()
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	held, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { held.Close() }) }
	t.Cleanup(release)
	if err := s.usage.start(30, false); err != nil {
		t.Fatal(err)
	}
	waitUsageCondition(t, func() bool { return s.db.Stats().WaitCount > 0 })
	return s, release
}

func waitUsageCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("observation synchronization did not reach the expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func saturatedUsageStore(t *testing.T) *Store {
	t.Helper()
	s, _ := blockedUsageStore(t)
	for range cap(s.usage.events) {
		s.usage.startGET("1", "")
	}
	if len(s.usage.events) != cap(s.usage.events) {
		t.Fatal("observation queue is not full")
	}
	return s
}

func TestUsageSynchronizationCancellationDoesNotBlockGET(t *testing.T) {
	for _, operation := range []string{"flush", "import"} {
		for _, full := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/barrier_wait", true: "/queue_full"}[full], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s, release := blockedUsageStore(t)
					if full {
						for range cap(s.usage.events) {
							s.usage.startGET("1", "")
						}
					}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan error, 1)
					go func() {
						if operation == "import" {
							done <- s.ImportSongUsage(ctx, historyAt(42, time.Now().UnixMilli()), true)
						} else {
							done <- s.usage.flush(ctx)
						}
					}()
					synctest.Wait()
					wantQueued := 1
					if full {
						wantQueued = cap(s.usage.events)
					}
					if len(s.usage.events) != wantQueued {
						t.Fatal("synchronization did not reach the expected queue wait")
					}
					getDone := make(chan struct{})
					go func() {
						at := s.usage.startGET("2", "")
						s.usage.record(usageEvent{songID: "2", at: at, method: "GET"})
						close(getDone)
					}()
					select {
					case <-getDone:
					case <-time.After(250 * time.Millisecond):
						t.Error("GET observation waited for synchronization")
					}
					cancel()
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Error("synchronization ignored cancellation", err)
						}
					case <-time.After(250 * time.Millisecond):
						t.Error("synchronization waited for the occupied database after cancellation")
					}
					release()
					if err := s.usage.flush(t.Context()); err != nil {
						t.Fatal(err)
					}
					if operation == "import" {
						var count int
						if err := s.db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=42").Scan(&count); err != nil || count != 0 {
							t.Errorf("canceled queued import committed: count=%d err=%v", count, err)
						}
					}
				})
			})
		}
	}
}

func TestUsageImportAppliesBetweenEarlierAndLaterGET(t *testing.T) {
	s, release := blockedUsageStore(t)
	s.usage.startGET("99", "")
	done := make(chan error, 1)
	go func() {
		done <- s.ImportSongUsage(t.Context(), historyAt(42, time.Now().Add(-time.Minute).UnixMilli()), true)
	}()
	waitUsageCondition(t, func() bool { return len(s.usage.events) == 2 })
	getDone := make(chan struct{})
	go func() {
		s.usage.startGET("42", "")
		close(getDone)
	}()
	select {
	case <-getDone:
	case <-time.After(250 * time.Millisecond):
		t.Error("later GET could not enqueue while the import waited")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.usage.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=42").Scan(&count); err != nil || count != 2 {
		t.Errorf("later GET did not accumulate after replacement: count=%d err=%v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=99").Scan(&count); err != nil || count != 0 {
		t.Errorf("earlier GET was not replaced: count=%d err=%v", count, err)
	}
}

func TestSongUsageCountReadsCommittedStateWithoutFlushing(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(resume) })
	// Pause the worker after a real detail commit, without occupying the
	// database connection or replacing the production synchronization path.
	s.usage.onHistoryChange = func(bool) {
		close(paused)
		<-resume
	}
	if err := s.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	s.usage.record(usageEvent{at: time.Now().UnixMilli(), method: "HEAD"})
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("collector did not pause at ordered operation")
	}
	s.usage.startGET("42", "")
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if count, err := s.SongUsageCount(ctx); err != nil || count != 0 {
		t.Errorf("status did not read committed state: count=%d err=%v", count, err)
	}
	once.Do(func() { close(resume) })
	if err := s.usage.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count, err := s.SongUsageCount(t.Context()); err != nil || count != 1 {
		t.Errorf("committed GET missing from status: count=%d err=%v", count, err)
	}
}

func TestCanceledSongUsageCountDoesNotBlockGET(t *testing.T) {
	s := saturatedUsageStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	statusDone := make(chan error, 1)
	go func() {
		_, err := s.SongUsageCount(ctx)
		statusDone <- err
	}()
	select {
	case err := <-statusDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("status error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("canceled status query waited for the observation queue")
	}
	getDone := make(chan struct{})
	go func() {
		s.usage.startGET("1", "")
		close(getDone)
	}()
	select {
	case <-getDone:
	case <-time.After(250 * time.Millisecond):
		t.Error("GET observation blocked behind status query with a full queue")
	}
}
