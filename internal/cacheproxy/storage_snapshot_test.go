package cacheproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageSnapshotIncludesWALAndCleansUp(t *testing.T) {
	s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(s.path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("no committed WAL", err)
	}
	path, cleanup, err := requestExportSnapshot(t.Context(), s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	db, err := readDatabaseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := readSongPolicy(t.Context(), db, 42)
	db.Close()
	if err != nil || policy != SongFavorite {
		t.Fatal(policy, err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("snapshot directory survived cleanup", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := requestExportSnapshot(ctx, s.path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
