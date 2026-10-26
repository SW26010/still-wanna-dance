package cacheproxy

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Exercise offline backup of the existing v5 format, including every business
// table. This is an acceptance fixture, not a new product backup mechanism.
func TestV5OfflineBackupRestoresAllBusinessData(t *testing.T) {
	golden, err := os.ReadFile("testdata/storage-v5.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "original.sqlite")
	if err := os.WriteFile(source, golden, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	// Complete the four empty tables in the frozen sample without changing DDL.
	if _, err := store.db.Exec(`
INSERT INTO catalog_checks VALUES('songs',1792960000000,'fixture check','offline');
INSERT INTO catalog_members VALUES(42);
INSERT INTO media_access SELECT md5,1792960000000 FROM media;
INSERT INTO song_playback VALUES(42,7,1792960000000);`); err != nil {
		t.Fatal(err)
	}
	before := snapshotBusinessTables(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	restored := filepath.Join(t.TempDir(), "中文 还原", "personal.sqlite")
	copyClosedDatabase := func(from, to string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			data, err := os.ReadFile(from + suffix)
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(to+suffix, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	copyClosedDatabase(source, backup)
	copyClosedDatabase(backup, restored)
	store, err = OpenStore(restored)
	if err != nil {
		t.Fatal(err)
	}
	assertPreserved := func() {
		t.Helper()
		if got := snapshotBusinessTables(t, store.db); !reflect.DeepEqual(got, before) {
			t.Fatal("restored business rows or column values changed")
		}
		if err := validateStorageDatabase(store.db); err != nil {
			t.Fatal("v5 format changed", err)
		}
		var integrity string
		if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatal("restored database integrity", integrity, err)
		}
		rows, err := store.db.Query("PRAGMA foreign_key_check")
		if err != nil {
			t.Fatal(err)
		}
		broken := rows.Next()
		err = rows.Err()
		rows.Close()
		if broken || err != nil {
			t.Fatal("restored business relations", broken, err)
		}
	}
	assertPreserved()
	// Metadata remains usable before a separately chosen video location exists.
	videoDir := filepath.Join(t.TempDir(), "另置视频")
	page, err := ReadLibraryPage(t.Context(), LibraryPaths{DatabasePath: restored, VideoDir: videoDir}, LibraryQuery{})
	if err != nil || page.Total != 1 || page.VideoError == "" || page.Songs[0].Policy != SongFavorite {
		t.Fatal("restored personal data depended on video availability", page, err)
	}
	cfg := fixtureConfig()
	cfg.Store, cfg.RequestRetentionDays = store, 0
	prepareFixtureVideo(t, &cfg, videoDir)
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	assertPreserved()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(original, saved) {
		t.Fatal("restore modified the original or backup", err)
	}
}

func snapshotBusinessTables(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(names) != 14 {
		t.Fatal("unexpected v5 table set", names, err)
	}
	result := make(map[string]string)
	for _, name := range names {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var values [][]any
		for rows.Next() {
			row := make([]any, len(columns))
			args := make([]any, len(columns))
			for i := range row {
				args[i] = &row[i]
			}
			if err := rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			values = append(values, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(values) == 0 {
			t.Fatal("fixture must cover every business table", name, err)
		}
		data, err := json.Marshal(struct {
			Columns []string
			Values  [][]any
		}{columns, values})
		if err != nil {
			t.Fatal(err)
		}
		result[name] = string(data)
	}
	return result
}
