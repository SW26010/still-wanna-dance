package cacheproxy

import (
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mediaseed "still-wanna-dance/data/media-seed"
)

func TestMediaOnlyImportUsesActualBudgetAndMappingScore(t *testing.T) {
	s, cfg := setup(t, nil)
	orphan, mapped := "orphan-A", "mapped-B"
	a, b := fmt.Sprintf("%x", md5.Sum([]byte(orphan))), fmt.Sprintf("%x", md5.Sum([]byte(mapped)))
	if err := insertSeedFixture(s.Store, seedJSON(t, "20261029000000", []string{a, b}, mediaseed.Mapping{ID: 1, MD5: b})); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.usage.write([]usageEvent{{songID: "1", at: time.Now().UnixMilli(), summaryOnly: true}}); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.Store.KnownMedia(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, body := range []string{orphan, mapped} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("%d.mp4", i))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if receipt, err := s.ImportVideo(t.Context(), path, allowed); err != nil || receipt == nil {
			t.Fatal(receipt, err)
		}
	}
	setRetentionLimit(t, s, int64(len(mapped)))
	s.trimCache()
	expectRetained(t, cfg.videoFile(a), false)
	expectRetained(t, cfg.videoFile(b), true)
}

func seedJSON(t *testing.T, version string, media []string, mappings ...mediaseed.Mapping) string {
	t.Helper()
	resources := make([]mediaseed.Media, 0, len(media))
	for _, key := range media {
		resources = append(resources, mediaseed.Media{MD5: key})
	}
	body, err := json.Marshal(mediaseed.Snapshot{Version: version, Media: resources, SongMedia: mappings})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func insertSeedFixture(s *Store, seed string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := initializeMediaSeed(tx, seed); err != nil {
		return err
	}
	return tx.Commit()
}

func TestBundledMediaSeedOnlyOnCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.sqlite")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for table, want := range map[string]int{"media": 10446, "song_media": 10471, "songs": 10471, "catalog_members": 0, "media_urls": 0, "song_usage": 0, "catalog_state": 0} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, want, err)
		}
	}
	// An existing library may deliberately have no seed rows. Reopening, format
	// selection and read-only inspection must preserve it, never reseed it.
	if _, err := s.db.Exec("DELETE FROM song_media; DELETE FROM songs; DELETE FROM media"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckDatabaseSelection(path); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM media").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if policy, err := s.SongPolicies(t.Context()); err != nil || policy[42] != SongFavorite {
		t.Fatal(policy, err)
	}
}

func TestCreationSeedFailureRollsBackSchemaAndVersion(t *testing.T) {
	key := strings.Repeat("a", 32)
	for _, seed := range []string{"{}", seedJSON(t, "bad", []string{key}), seedJSON(t, "20261029000000", []string{key, key}), seedJSON(t, "20261029000000", []string{key}, mediaseed.Mapping{ID: 1, MD5: strings.Repeat("b", 32)})} {
		path := filepath.Join(t.TempDir(), "failed.sqlite")
		db, err := sql.Open("sqlite", filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		if err := initializeStorageFromSeed(db, seed); err == nil {
			t.Fatal("invalid seed initialized database")
		}
		if got := migrationRows(t, db, "PRAGMA user_version"); got != "[0]\n" {
			t.Fatal(got)
		}
		if got := migrationRows(t, db, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'"); got != "[0]\n" {
			t.Fatal(got)
		}
		db.Close()
		if store, err := OpenStore(path); err == nil {
			store.Close()
			t.Fatal("failed creation accepted")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("recovery file removed", err)
		}
	}
}
