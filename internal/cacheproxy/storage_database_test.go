package cacheproxy

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageContractRejectsIncompatibleFilesEverywhere(t *testing.T) {
	for _, mutation := range []string{"PRAGMA user_version=999", "PRAGMA application_id=123", "PRAGMA user_version=0", "PRAGMA user_version=1", "PRAGMA user_version=2", "PRAGMA user_version=3", "PRAGMA user_version=4", "PRAGMA user_version=5", "DROP TABLE song_preferences", "DROP TABLE catalog_checks", "DROP INDEX request_events_time", "ALTER TABLE songs ADD COLUMN surprise TEXT"} {
		t.Run(mutation, func(t *testing.T) {
			cfg := fixtureConfig()
			prepareFixtureResources(t, &cfg, t.TempDir(), true)
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.usage.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if err := cfg.Store.Close(); err != nil {
				t.Fatal(err)
			}
			path := cfg.Store.path
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			partial := filepath.Join(cfg.publishDir(), "download-publish-123.part")
			writeTestFile(t, partial, "in progress")
			ctx := context.Background()
			checks := map[string]func() error{
				"startup": func() error {
					store, err := OpenStore(path)
					if store != nil {
						store.Close()
					}
					return err
				},
				"catalog": func() error { _, err := LoadCatalogStatus(ctx, cfg.Store.path); return err },
				"cache": func() error {
					_, err := ReadLibraryPage(ctx, cfg.libraryPaths(), LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "", Offset: 0})
					return err
				},
				"recent": func() error { _, err := ReadRecentRequests(ctx, cfg.Store.path, 50); return err },
				"delete": func() error {
					_, err := DeleteCacheOffline(ctx, cfg.libraryPaths(), []CacheSelection{{Key: strings.Repeat("a", 32)}})
					return err
				},
			}
			for name, check := range checks {
				if err := check(); err == nil {
					t.Errorf("%s accepted incompatible database", name)
				}
			}
			if ReadTrafficStats(cfg.Store.path).Error == "" {
				t.Error("traffic accepted incompatible database")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("incompatible database changed", err)
			}
			if _, err := os.Stat(partial); err != nil {
				t.Fatal("startup cleaned partial before rejecting database", err)
			}
		})
	}
}

func TestRecentReaderNeverRepairsSchema(t *testing.T) {
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("DROP INDEX request_events_time"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := cfg.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecentRequests(context.Background(), cfg.Store.path, 50); err == nil {
		t.Fatal("reader silently repaired missing index")
	}
	db, err := sql.Open("sqlite", cfg.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='request_events_time'").Scan(&count)
	db.Close()
	if err != nil || count != 0 {
		t.Fatal("read changed schema", count, err)
	}
	store, err := OpenStore(cfg.Store.path)
	if store != nil {
		store.Close()
	}
	if err == nil {
		t.Fatal("writer silently repaired schema")
	}
}
func TestStorageInitializationIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := fixtureConfig()
	prepareFixtureResources(t, &cfg, root, true)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var identity, version int
	if err := s.usage.db.QueryRow("PRAGMA application_id").Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if err := s.usage.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if identity != storageApplicationID || version != storageSchemaVersion {
		t.Fatal(identity, version)
	}
}

func TestStorageRejectsExistingEmptyFile(t *testing.T) {
	path := storagePath(t.TempDir())
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, writable := range []bool{false, true} {
		db, err := openFixtureDatabase(path, writable)
		if db != nil {
			db.Close()
		}
		if err == nil {
			t.Fatal("accepted empty existing database")
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) != 0 {
			t.Fatal("modified empty file", err)
		}
	}
}

func TestStorageInitializationRollsBack(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A conflicting final table fails after the earlier DDL has executed.
	if _, err := db.Exec("CREATE TABLE traffic_totals (sentinel TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := initializeStorage(db); err == nil {
		t.Fatal("expected initialization failure")
	}
	var objects, version, identity int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("PRAGMA user_version").Scan(&version)
	db.QueryRow("PRAGMA application_id").Scan(&identity)
	if objects != 1 || version != 0 || identity != 0 {
		t.Fatal(objects, version, identity)
	}
}
