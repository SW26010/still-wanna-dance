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
	for _, mutation := range []string{"PRAGMA user_version=999", "PRAGMA application_id=123", "PRAGMA user_version=0"} {
		t.Run(mutation, func(t *testing.T) {
			cfg := fixtureConfig()
			cfg.StorageDir = t.TempDir()
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.usage.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			s.Close()
			path := storagePath(cfg.StorageDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			partial := filepath.Join(cfg.StorageDir, "tmp", "download-preserve.part")
			writeTestFile(t, partial, "in progress")
			ctx := context.Background()
			checks := map[string]func() error{
				"startup": func() error {
					s, err := New(cfg)
					if s != nil {
						s.Close()
					}
					return err
				},
				"scan":    func() error { _, err := LoadScanTargets(ctx, cfg.StorageDir); return err },
				"catalog": func() error { _, err := LoadCatalogStatus(ctx, cfg.StorageDir); return err },
				"cache":   func() error { _, err := ReadCachePage(ctx, cfg.StorageDir, "", "", 0); return err },
				"recent":  func() error { _, err := ReadRecentRequests(ctx, cfg.StorageDir, 50); return err },
				"delete": func() error {
					_, err := DeleteCacheOffline(ctx, cfg.StorageDir, []CacheSelection{{Key: strings.Repeat("a", 32)}})
					return err
				},
			}
			for name, check := range checks {
				if err := check(); err == nil {
					t.Errorf("%s accepted incompatible database", name)
				}
			}
			if ReadTrafficStats(cfg.StorageDir).Error == "" {
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
	cfg.StorageDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("DROP INDEX request_events_http_time"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := ReadRecentRequests(context.Background(), cfg.StorageDir, 50); err == nil {
		t.Fatal("reader silently repaired missing index")
	}
	db, err := sql.Open("sqlite", storagePath(cfg.StorageDir))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='request_events_http_time'").Scan(&count)
	db.Close()
	if err != nil || count != 0 {
		t.Fatal("read changed schema", count, err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := ReadRecentRequests(context.Background(), cfg.StorageDir, 50); err != nil {
		t.Fatal(err)
	}
}

func TestStorageInitializationIdentityAndEmptyRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(storagePath(root), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfig()
	cfg.StorageDir = root
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
