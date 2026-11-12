package cacheproxy

import (
	"bytes"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionReconciliationFailureDoesNotWaitForDecision(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		t.Run(fmt.Sprintf("unlimited=%v", unlimited), func(t *testing.T) {
			s := retentionChangeEngine(t)
			s.cfg.VideoDir = filepath.Join(t.TempDir(), "missing")
			s.cfg.CacheUnlimited = unlimited
			var logs bytes.Buffer
			s.cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			// A mapping/preference decision can be active while inventory I/O
			// discovers a directory failure. Reporting it needs no policy lock.
			s.Store.retentionRunMu.Lock()
			done := make(chan struct{})
			defer func() {
				s.Store.retentionRunMu.Unlock()
				<-done
			}()
			go func() { s.runRetention(true); close(done) }()
			select {
			case <-done:
				if !strings.Contains(logs.String(), "cache_eviction_failed") {
					t.Fatal("inventory error was not reported", logs.String())
				}
			case <-time.After(time.Second):
				t.Fatal("directory reconciliation waited for a policy decision")
			}
		})
	}
}

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
