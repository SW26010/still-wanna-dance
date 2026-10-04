package cacheproxy

import (
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
