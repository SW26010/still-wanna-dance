package cacheproxy

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func waitForDatabaseWait(t *testing.T, db *sql.DB, before int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for db.Stats().WaitCount <= before {
		if time.Now().After(deadline) {
			t.Fatal("operation did not reach the occupied database connection")
		}
		time.Sleep(time.Millisecond)
	}
}

func requirePinAndRelease(t *testing.T, s *Server, v video) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.pinVideo(v)
		s.releaseVideo(v)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pin/release blocked behind unrelated I/O")
	}
}

func TestSupersededCleanupRechecksPinsAfterDatabaseWait(t *testing.T) {
	s, cfg := setup(t, nil)
	v := parsedVideo(t, s, payload)
	writeTestFile(t, cfg.videoFile(v.key), payload)
	if err := s.recordVideo(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec(`INSERT INTO songs(song_id) VALUES ('42');
 INSERT INTO song_videos(song_id, version_key) VALUES ('42', ?)`, v.key); err != nil {
		t.Fatal(err)
	}
	conn, err := s.usage.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waits := s.usage.db.Stats().WaitCount
	// Release must return even though the background ownership query cannot.
	requirePinAndRelease(t, s, v)
	waitForDatabaseWait(t, s.usage.db, waits)
	requirePinAndRelease(t, s, video{key: "other"})
	pinned := make(chan struct{})
	go func() { s.pinVideo(v); close(pinned) }()
	select {
	case <-pinned:
	case <-time.After(time.Second):
		t.Fatal("cleanup query held the global pin lock")
	}
	active := true
	defer func() {
		if active {
			s.releaseVideo(v)
		}
	}()
	conn.Close()
	s.runRetention(false)
	expectRetained(t, cfg.videoFile(v.key), true)
	s.releaseVideo(v)
	active = false
	expectRetained(t, cfg.videoFile(v.key), false)
}

func TestMappingTransactionDoesNotHoldRetentionLock(t *testing.T) {
	s, _ := setup(t, nil)
	old := parsedVideo(t, s, "old")
	next := parsedVideo(t, s, payload)
	for _, v := range []video{old, next} {
		if err := s.recordVideo(context.Background(), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.recordSongVideo(context.Background(), "42", old); err != nil {
		t.Fatal(err)
	}
	s.runRetention(false)
	// Keep the cleanup worker out so the only connection waiter is BeginTx.
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	occupied := make(chan *sql.Conn, 1)
	s.cfg.ResolveCurrent = func(ctx context.Context, id string) (string, error) {
		conn, err := s.usage.db.Conn(ctx)
		occupied <- conn
		return videoURL(payload), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waits := s.usage.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- s.recordSongVideo(ctx, "42", next) }()
	var conn *sql.Conn
	select {
	case conn = <-occupied:
		if conn == nil {
			t.Fatal("could not occupy database connection")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer conn.Close()
	waitForDatabaseWait(t, s.usage.db, waits)
	requirePinAndRelease(t, s, old)
	conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertSongResource(t, s, "42", next.key)
}

func TestRemovalReservationOnlyBlocksSameVideo(t *testing.T) {
	s, _ := setup(t, nil)
	v := parsedVideo(t, s, payload)
	s.retentionMu.Lock()
	reserved := s.beginVideoRemovalLocked(v.key)
	s.retentionMu.Unlock()
	if !reserved {
		t.Fatal("could not reserve unpinned video")
	}
	defer func() {
		if reserved {
			s.finishVideoRemoval(v.key, false)
		}
	}()
	pinned := make(chan struct{})
	go func() { s.pinVideo(v); close(pinned) }()
	requirePinAndRelease(t, s, video{key: "other"})
	select {
	case <-pinned:
		t.Fatal("pin acquired during deletion")
	case <-time.After(20 * time.Millisecond):
	}
	s.finishVideoRemoval(v.key, true)
	reserved = false
	select {
	case <-pinned:
		s.releaseVideo(v)
	case <-time.After(time.Second):
		t.Fatal("pin did not resume after deletion")
	}
}
