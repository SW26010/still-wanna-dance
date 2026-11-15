package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestUsageRecoversLockedBatchAndPersistsAfterReopen(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reader, err := readDatabaseFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var rows int
	if err := tx.QueryRow("SELECT count(*) FROM request_events").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// Keep the production busy timeout. Release the read lock only after the
	// collector reports an actual failed attempt, rather than relying on sleep.
	conflict := make(chan struct{})
	var once sync.Once
	s.usage.log = slog.New(flightEventHandler{Handler: slog.NewTextHandler(io.Discard, nil), handle: func(r slog.Record) {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "event_name" && (a.Value.String() == "usage_write_retry" || a.Value.String() == "usage_write_failed") {
				once.Do(func() { close(conflict) })
			}
			return true
		})
	}})
	if err := s.usage.start(0, true); err != nil {
		t.Fatal(err)
	}
	at := s.usage.startGET("42", "")
	s.usage.record(usageEvent{at: at, songID: "42", method: "GET", outcome: "completed"})
	select {
	case <-conflict:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not reach a real SQLite lock conflict")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s.usage.flush()
	if err := s.Close(); err != nil {
		t.Errorf("recovered collector retained a failure: %v", err)
	}
	reopened, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRowContext(context.Background(), "SELECT count(*) FROM request_events WHERE song_id=42 AND requested_at=?", at).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("persisted details=%d, want 1: %v", rows, err)
	}
	var demands int
	var score float64
	if err := reopened.db.QueryRow("SELECT demand_count,demand_score FROM song_usage WHERE song_id=42").Scan(&demands, &score); err != nil || demands != 1 || score != 1 {
		t.Errorf("persisted demand=%d score=%g, want 1/1: %v", demands, score, err)
	}
}

func TestUsageLockRecoveryIsBoundedAndRollsBackWholeBatch(t *testing.T) {
	s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.usage.unlimited = true
	reader, err := readDatabaseFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var rows int
	if err := tx.QueryRow("SELECT count(*) FROM request_events").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	retries := 0
	s.usage.log = slog.New(flightEventHandler{Handler: slog.NewTextHandler(io.Discard, nil), handle: func(r slog.Record) {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "event_name" && a.Value.String() == "usage_write_retry" {
				retries++
			}
			return true
		})
	}})
	batch := []usageEvent{{at: 1000, songID: "42", summaryOnly: true}, {at: 1000, songID: "42", method: "GET"}, {at: 41000, songID: "42", summaryOnly: true}, {at: 41000, songID: "42", method: "GET"}}
	if err := s.usage.writeBatch(batch); !isStorageBusy(err) || retries != 2 {
		t.Fatalf("bounded attempts: retries=%d err=%v", retries, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM request_events").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed batch leaked details: %d %v", rows, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM song_usage").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed batch leaked demand: %d %v", rows, err)
	}
	if err := s.usage.writeBatch(batch); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM request_events").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("replayed details: %d %v", rows, err)
	}
	if err := s.db.QueryRow("SELECT demand_count FROM song_usage WHERE song_id=42").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("replayed demand order/count: %d %v", rows, err)
	}
	if err := s.usage.writeBatch([]usageEvent{{songID: "invalid"}}); err == nil || retries != 2 {
		t.Fatalf("validation failure was retried: %d %v", retries, err)
	}
}
