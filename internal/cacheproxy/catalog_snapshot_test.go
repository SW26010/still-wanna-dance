package cacheproxy

import (
	"context"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCatalogInspectionKeepsConcurrentSnapshotsCoherent(t *testing.T) {
	s, cfg := setup(t, nil)
	key := strings.Repeat("a", 32)
	writeTestFile(t, cfg.videoFile(key), "existing video")
	candidate := func(id int64) Catalog {
		return testCandidate(fmt.Sprintf("202610290000%02d", id), CatalogSong{ID: id, MD5: key})
	}
	if err := s.Store.SyncCatalog(t.Context(), candidate(1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for id := int64(2); id <= 30; id++ {
			if err := s.Store.SyncCatalog(t.Context(), candidate(id)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for range 50 {
		v, err := s.Store.InspectLocalCatalog(t.Context(), cfg.VideoDir)
		if err != nil || len(v.Songs) != 1 {
			t.Fatal(v, err)
		}
		id := v.Songs[0].ID
		if v.Status.Revision != candidate(id).Revision || !v.Current[fmt.Sprint(id)] || v.Files.References[fmt.Sprint(id)] != key || !v.Files.Present[key] {
			t.Fatal("coverage mixed members, mappings or watermark across commits", v)
		}
	}
}

func TestStoreCatalogSyncSurvivesActiveEngineVideoFailure(t *testing.T) {
	s, cfg := setup(t, nil)
	s.storageError(os.ErrNotExist) // Inject a video I/O fault without damaging the database.
	candidate := testCandidate("20261029000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})
	if err := s.Store.SyncCatalog(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "offline-videos")
	v, err := s.Store.InspectLocalCatalog(t.Context(), missing)
	if !os.IsNotExist(err) || v.Status.Revision != candidate.Revision || len(v.Songs) != 1 {
		t.Fatal("video error lost metadata or was reported as missing files", v, err)
	}
	if s.Store.Error() != nil || s.StorageError() == nil {
		t.Fatal("database and engine faults were conflated")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created video directory", err)
	}
	if _, err := s.Store.InspectLocalCatalog(t.Context(), cfg.VideoDir); err != nil {
		t.Fatal(err)
	}
	if s.StorageError() == nil {
		t.Fatal("inspection cleared the engine failure")
	}
}

func TestCatalogVersionAndMembersSurviveRestart(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, testCandidate("20261001000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})); err != nil {
		t.Fatal(err)
	}
	before, err := s.CatalogStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopenFixtureStore(t, &cfg)
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.CatalogStatus(ctx)
	if err != nil || after.Revision != before.Revision {
		t.Fatal(before, after, err)
	}
	local, release, err := reopened.ReadLocalCatalog(ctx)
	defer release()
	if err != nil || !local.Current["1"] || len(local.Songs) != 1 {
		t.Fatal(local, err)
	}
}

func TestLoadCatalogStatusRejectsMissingTrackingSchema(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	c := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.usage.db.Exec("DROP TABLE catalog_state"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCatalogStatus(ctx, cfg.Store.path)
	if err == nil {
		t.Fatal(got, err)
	}
}
