package cacheproxy

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyStorageMarkerPreventsInitializationCleanupAndDeletion(t *testing.T) {
	for _, current := range []bool{false, true} {
		name := "legacy_only"
		if current {
			name = "both_databases"
		}
		t.Run(name, func(t *testing.T) {
			cfg := fixtureConfig()
			cfg.StorageDir = t.TempDir()
			key := strings.Repeat("a", 32)
			selected := CacheSelection{Key: key}
			if current {
				s, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.SyncCatalog(context.Background(), testCandidate("20261007000000", CatalogSong{ID: 1, MD5: key})); err != nil {
					s.Close()
					t.Fatal(err)
				}
				s.Close()
			}
			writeTestFile(t, cfg.videoFile(key), "preserve video larger than capacity")
			if current {
				db, err := cacheReadDB(cfg.StorageDir)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := readCacheEntry(context.Background(), cfg.StorageDir, key, db)
				db.Close()
				if err != nil || !entry.Known || entry.Stamp == "" {
					t.Fatal("invalid deletion fixture", entry, err)
				}
				selected.Stamp = entry.Stamp
			}
			// Even an unreadable-as-SQLite marker must reject the root without
			// attempting to parse, recover or migrate the old database.
			writeTestFile(t, filepath.Join(cfg.StorageDir, "stepstash.sqlite"), "legacy database marker")
			writeTestFile(t, filepath.Join(cfg.StorageDir, "stepstash.sqlite-journal"), "legacy journal")
			writeTestFile(t, filepath.Join(cfg.tempDir(), "download-preserve.part"), "partial download")
			snapshot := func() map[string]string {
				t.Helper()
				files := map[string]string{}
				err := filepath.WalkDir(cfg.StorageDir, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() || entry.Name() == ".lock" {
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					files[path] = string(data)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				return files
			}
			before := snapshot()
			cfg.MaxCacheBytes = 1
			// Repeated attempts also prove rejection releases the directory lock.
			for range 2 {
				s, err := New(cfg)
				if s != nil {
					s.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "stepstash.sqlite") {
					t.Fatal("startup did not reject legacy marker", err)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("startup created, changed or removed storage files")
				}
				_, err = DeleteCacheOffline(context.Background(), cfg.StorageDir, []CacheSelection{selected})
				if err == nil || !strings.Contains(err.Error(), "stepstash.sqlite") {
					t.Fatal("offline deletion did not reject legacy marker", err)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("offline deletion changed storage files")
				}
			}
		})
	}
}

func TestLegacyStorageMarkerDirectoryAlsoRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "stepstash.sqlite"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkStorageLayout(root); err == nil || !strings.Contains(err.Error(), "stepstash.sqlite") {
		t.Fatal(err)
	}
}
