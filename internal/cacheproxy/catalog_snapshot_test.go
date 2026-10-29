package cacheproxy

import (
	"context"
	"errors"
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

func TestLocalCatalogChecksAndSnapshot(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	c := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: a})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Simulate a prior check and ensure identical versions still advance it.
	if _, err := s.usage.db.Exec("UPDATE catalog_checks SET checked_at=1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	first, release, err := s.ReadLocalCatalog(ctx)
	defer release()
	if err != nil || first.Status.CheckedAt.UnixMilli() <= 1 || first.Files.References["1"] != a {
		t.Fatal(first, err)
	}
	// An operation retains one coherent snapshot across a concurrent update.
	c.Revision = "20261002000000"
	c.Songs[0].MD5 = b
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	if first.Status.Revision != "20261001000000" || first.Songs[0].MD5 != a || first.Files.References["1"] != a {
		t.Fatal(first)
	}
	if err := s.RecordCatalogError(ctx, errors.New("HTTP 500")); err != nil {
		t.Fatal(err)
	}
	status, err := s.CatalogStatus(ctx)
	if err != nil || status.Error != "HTTP 500" {
		t.Fatal(status, err)
	}
	checked := status.CheckedAt
	conflict := testCandidate(c.Revision, CatalogSong{ID: 3, MD5: a})
	if err := s.SyncCatalog(ctx, conflict); err == nil {
		t.Fatal("accepted conflict")
	}
	status, _ = s.CatalogStatus(ctx)
	if !status.CheckedAt.Equal(checked) {
		t.Fatal("conflict advanced check time")
	}
	if _, err := s.usage.db.Exec("UPDATE catalog_checks SET checked_at=1"); err != nil {
		t.Fatal(err)
	}
	old := testCandidate("20260901000000", CatalogSong{ID: 1, MD5: a})
	if err := s.SyncCatalog(ctx, old); !errors.Is(err, ErrCatalogOlder) {
		t.Fatal(err)
	}
	status, _ = s.CatalogStatus(ctx)
	if status.Revision != c.Revision || status.CheckedAt.UnixMilli() <= 1 || status.Error != "" || !strings.Contains(status.Message, "较旧") {
		t.Fatal(status)
	}

}

func TestCatalogCheckStatusSurvivesRestart(t *testing.T) {
	s, cfg := setup(t, nil)
	ctx := context.Background()
	if err := s.SyncCatalog(ctx, testCandidate("20261001000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCatalogError(ctx, errors.New("HTTP 500")); err != nil {
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
	if err != nil || after.Revision != before.Revision || after.Error != before.Error || !after.CheckedAt.Equal(before.CheckedAt) {
		t.Fatal(before, after, err)
	}
	local, release, err := reopened.ReadLocalCatalog(ctx)
	defer release()
	if err != nil || !local.Current["1"] || len(local.Songs) != 1 {
		t.Fatal(local, err)
	}
}

func TestCatalogCheckAndRollback(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	c := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Clearing diagnostic history does not alter accepted membership.
	for _, query := range []string{"DELETE FROM catalog_checks"} {
		if _, err := s.usage.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.CatalogStatus(ctx)
	if err != nil || !status.CheckedAt.IsZero() || status.Revision != c.Revision {
		t.Fatal(status, err)
	}
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	before, err := s.CatalogStatus(ctx)
	if err != nil || before.CheckedAt.IsZero() {
		t.Fatal(before, err)
	}
	if _, err := s.usage.db.Exec(`CREATE TRIGGER reject_catalog_check BEFORE UPDATE ON catalog_checks BEGIN SELECT RAISE(ABORT, 'check write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	newer := testCandidate("20261002000000", CatalogSong{ID: 2, MD5: strings.Repeat("b", 32)})
	if err := s.SyncCatalog(ctx, newer); err == nil {
		t.Fatal("expected transaction failure")
	}
	local, release, err := s.ReadLocalCatalog(ctx)
	defer release()
	if err != nil || local.Status != before || len(local.Songs) != 1 || local.Songs[0].ID != 1 || !local.Current["1"] || local.Current["2"] {
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
	if _, err := s.usage.db.Exec("DROP TABLE catalog_checks"); err != nil {
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
