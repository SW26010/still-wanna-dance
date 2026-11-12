package cacheproxy

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMetadataChangesIgnoreRepeatedObservationsAndRollback(t *testing.T) {
	s := retentionChangeEngine(t)
	store, ctx := s.Store, t.Context()
	observation := PlaybackObservation{SongID: 1, Node: "cf", URL: "http://play.udon.dance/files/1/1-video.mp4?e=" + strings.Repeat("a", 32) + "&s=1234"}
	if err := store.ObserveSongURL(ctx, observation); err != nil {
		t.Fatal(err)
	}
	requireRetentionWake(t, s, true)
	previous := store.MetadataUpdatedAt()
	if previous.IsZero() {
		t.Fatal("committed mapping did not publish a change")
	}
	if err := store.ObserveSongURL(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if got := store.MetadataUpdatedAt(); got != previous {
		t.Fatal("repeated observation published a change", previous, got)
	}
	requireRetentionWake(t, s, false)
	wantErr := errors.New("reject transaction after write")
	err := store.writeMapping(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO songs(id) VALUES (99)"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) || store.MetadataUpdatedAt() != previous {
		t.Fatal("rolled back write published a change", err, store.MetadataUpdatedAt())
	}
	requireRetentionWake(t, s, false)
	// SQLite's connection counter includes rolled-back writes. A later no-op
	// must still be compared only with its own transaction's starting counter.
	if err := store.ObserveSongURL(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if store.MetadataUpdatedAt() != previous {
		t.Fatal("rolled-back changes leaked into a subsequent observation")
	}
	// A different song can map to the same already-known address and resource.
	observation.SongID = 2
	time.Sleep(time.Millisecond)
	if err := store.ObserveSongURL(ctx, observation); err != nil {
		t.Fatal(err)
	}
	if !store.MetadataUpdatedAt().After(previous) {
		t.Fatal("new song mapping to an existing resource did not publish a change")
	}
	requireRetentionWake(t, s, true)
}

func TestFailedMetadataCommitDoesNotPublishChanges(t *testing.T) {
	s := retentionChangeEngine(t)
	ctx := t.Context()
	var wrote bool
	err := s.Store.writeMapping(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO song_media(song_id,md5) VALUES(99,?)", strings.Repeat("a", 32))
		wrote = err == nil
		return err
	})
	if !wrote || err == nil {
		t.Fatal("fixture did not reach a failed commit", wrote, err)
	}
	if !s.Store.MetadataUpdatedAt().IsZero() {
		t.Fatal("failed commit published a metadata change")
	}
	requireRetentionWake(t, s, false)
}
