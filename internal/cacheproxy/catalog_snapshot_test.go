package cacheproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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
	conflict := testCandidate(c.Revision, CatalogSong{ID: 1, MD5: a})
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

func TestCatalogCheckMigrationAndRollback(t *testing.T) {
	s, _ := setup(t, nil)
	ctx := context.Background()
	c := testCandidate("20261001000000", CatalogSong{ID: 1, MD5: strings.Repeat("a", 32)})
	if err := s.SyncCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Simulate an existing database that predates check tracking.
	for _, query := range []string{"DELETE FROM catalog_checks", "DELETE FROM catalog_members"} {
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

func TestLoadCatalogStatusBeforeTrackingSchema(t *testing.T) {
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
	got, err := LoadCatalogStatus(ctx, cfg.StorageDir)
	if err != nil || got.Revision != c.Revision || !got.CheckedAt.IsZero() || got.Error != "" {
		t.Fatal(got, err)
	}
}
