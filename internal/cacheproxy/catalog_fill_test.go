package cacheproxy

import (
	"errors"
	"fmt"
	"testing"
)

// BEFORE INSERT fires even for ON CONFLICT DO NOTHING. Rejecting attempts on
// existing rows catches the redundant SQL that total_changes() cannot see.
func TestCatalogFillOnlyInsertsMissingRows(t *testing.T) {
	for _, revision := range []string{"20261114000000", "20261115000000"} {
		t.Run(revision, func(t *testing.T) {
			s := retentionChangeEngine(t)
			ctx := t.Context()
			a, b, c := fmt.Sprintf("%032x", 1), fmt.Sprintf("%032x", 2), fmt.Sprintf("%032x", 3)
			initial := Catalog{Source: UdonCatalogURL, Revision: "20261115000000", Songs: []CatalogSong{{ID: 1, MD5: a}, {ID: 2}, {ID: 3}}}
			if err := s.Store.SyncCatalog(ctx, initial); err != nil {
				t.Fatal(err)
			}
			requireRetentionWake(t, s, true)
			for _, table := range []struct{ name, key string }{{"songs", "id"}, {"media", "md5"}, {"song_media", "song_id"}} {
				query := fmt.Sprintf("CREATE TRIGGER reject_existing_%s BEFORE INSERT ON %s WHEN EXISTS (SELECT 1 FROM %s WHERE %s=NEW.%s) BEGIN SELECT RAISE(ABORT,'redundant insert'); END", table.name, table.name, table.name, table.key, table.key)
				if _, err := s.Store.db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			backup := initial
			backup.Source, backup.Revision = KivaCatalogURL, revision
			var wantErr error
			if revision < initial.Revision {
				wantErr = ErrCatalogOlder
			}
			if err := s.Store.SyncCatalog(ctx, backup); !errors.Is(err, wantErr) {
				t.Fatalf("complete catalog attempted redundant inserts: %v", err)
			}
			requireRetentionWake(t, s, false)
			// A conflict still adds the new media. Shared new media is inserted
			// once; unresolved current songs and historical placeholders get links.
			backup.Songs = []CatalogSong{{ID: 1, MD5: c}, {ID: 2, MD5: b}, {ID: 3, MD5: b}, {ID: 4, MD5: a}, {ID: 5}}
			if err := s.Store.SyncCatalog(ctx, backup); !errors.Is(err, wantErr) {
				t.Fatalf("fill missing rows: %v", err)
			}
			requireRetentionWake(t, s, true)
			for id, key := range map[string]string{"1": a, "2": b, "3": b, "4": a} {
				assertSongResource(t, s, id, key)
			}
			var media, songs, members int
			if err := s.Store.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM media), (SELECT count(*) FROM songs), (SELECT count(*) FROM catalog_members)").Scan(&media, &songs, &members); err != nil || media != 3 || songs != 4 || members != 3 {
				t.Fatal(media, songs, members, err)
			}
			status, err := s.Store.CatalogStatus(ctx)
			if err != nil || status.Revision != initial.Revision || status.Source != initial.Source {
				t.Fatal(status, err)
			}
			if err := s.Store.SyncCatalog(ctx, backup); !errors.Is(err, wantErr) {
				t.Fatalf("repeat filled catalog: %v", err)
			}
			requireRetentionWake(t, s, false)
		})
	}
}

func TestCatalogFillRollback(t *testing.T) {
	for _, revision := range []string{"20261114000000", "20261115000000"} {
		t.Run(revision, func(t *testing.T) {
			s := retentionChangeEngine(t)
			ctx := t.Context()
			initial := Catalog{Source: UdonCatalogURL, Revision: "20261115000000", Songs: []CatalogSong{{ID: 1}}}
			if err := s.Store.SyncCatalog(ctx, initial); err != nil {
				t.Fatal(err)
			}
			requireRetentionWake(t, s, true)
			if _, err := s.Store.db.ExecContext(ctx, "CREATE TRIGGER reject_mapping BEFORE INSERT ON song_media WHEN NEW.song_id=2 BEGIN SELECT RAISE(ABORT,'injected mapping failure'); END"); err != nil {
				t.Fatal(err)
			}
			backup := Catalog{Source: KivaCatalogURL, Revision: revision, Songs: []CatalogSong{{ID: 1, MD5: fmt.Sprintf("%032x", 1)}, {ID: 2, MD5: fmt.Sprintf("%032x", 2)}}}
			if err := s.Store.SyncCatalog(ctx, backup); err == nil || errors.Is(err, ErrCatalogOlder) {
				t.Fatal("mapping failure was not returned", err)
			}
			requireRetentionWake(t, s, false)
			var songs, media, mappings int
			if err := s.Store.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM songs), (SELECT count(*) FROM media), (SELECT count(*) FROM song_media)").Scan(&songs, &media, &mappings); err != nil || songs != 1 || media != 0 || mappings != 0 {
				t.Fatal("partial fill escaped rollback", songs, media, mappings, err)
			}
			status, err := s.Store.CatalogStatus(ctx)
			if err != nil || status.Revision != initial.Revision || status.Source != initial.Source {
				t.Fatal(status, err)
			}
		})
	}
}
