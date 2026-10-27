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
			root := t.TempDir()
			cfg.VideoDir = filepath.Join(root, "videos")
			key := strings.Repeat("a", 32)
			selected := CacheSelection{Key: key}
			if current {
				store, err := CreateStore(storagePath(root))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SyncCatalog(context.Background(), testCandidate("20261007000000", CatalogSong{ID: 1, MD5: key})); err != nil {
					store.Close()
					t.Fatal(err)
				}
				store.Close()
			}
			writeTestFile(t, cfg.videoFile(key), "preserve video larger than capacity")
			if current {
				db, err := cacheReadDB(root)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := readCacheEntry(context.Background(), fixtureLibrary(root), key, db)
				db.Close()
				if err != nil || !entry.Known || entry.Stamp == "" {
					t.Fatal("invalid deletion fixture", entry, err)
				}
				selected.Stamp = entry.Stamp
			}
			// Even an unreadable-as-SQLite marker must reject the root without
			// attempting to parse, recover or migrate the old database.
			writeTestFile(t, filepath.Join(root, "stepstash.sqlite"), "legacy database marker")
			writeTestFile(t, filepath.Join(root, "stepstash.sqlite-journal"), "legacy journal")
			writeTestFile(t, filepath.Join(cfg.publishDir(), "download-publish-123.part"), "partial download")
			snapshot := func() map[string]string {
				t.Helper()
				files := map[string]string{}
				err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
			cfg.CacheUnlimited = false
			// Repeated ordinary opens and explicit creates both preserve the old root.
			for range 2 {
				store, err := OpenStore(storagePath(root))
				if store != nil {
					store.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "stepstash.sqlite") {
					t.Fatal("startup did not reject legacy marker", err)
				}
				if !reflect.DeepEqual(before, snapshot()) {
					t.Fatal("startup created, changed or removed storage files")
				}
				created, createErr := CreateStore(storagePath(root))
				if created != nil {
					created.Close()
				}
				if createErr == nil {
					t.Fatal("created a database in a legacy root")
				}
				_, err = DeleteCacheOffline(context.Background(), fixtureLibrary(root), []CacheSelection{selected})
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
	if err := checkDatabaseDirectory(root); err == nil || !strings.Contains(err.Error(), "stepstash.sqlite") {
		t.Fatal(err)
	}
}
