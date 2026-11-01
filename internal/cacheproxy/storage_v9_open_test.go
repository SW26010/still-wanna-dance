package cacheproxy

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStorageV9OpenMigratesOnceAfterReadOnlySelection(t *testing.T) {
	db, path := migrationFixture(t)
	if _, err := db.Exec("DELETE FROM catalog_state"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, _ := os.ReadFile(path)
	if err := CheckDatabaseSelection(path); err != nil {
		t.Fatal(err)
	}
	if db, err := readDatabaseFile(path); err == nil {
		db.Close()
		t.Fatal("v8 business read accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("selection changed v8")
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"songs", "media", "song_media"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("migration reseeded", table, count, err)
		}
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM catalog_state").Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented catalog state", count, err)
	}
	migrated, err := schemaObjects(store.db)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	fresh, err := CreateStore(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	created, err := schemaObjects(fresh.db)
	if err != nil || !reflect.DeepEqual(migrated, created) {
		t.Fatal("new and migrated schemas differ", err)
	}
	before, _ = os.ReadFile(path)
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	after, _ = os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("reopening v9 rewrote data")
	}
}

func TestStorageV8InvalidFrozenURLRejectedAtOpen(t *testing.T) {
	before, err := os.ReadFile("testdata/storage-v8.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "invalid.sqlite")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDatabaseSelection(path); err != nil {
		t.Fatal("selection should only inspect format", err)
	}
	for range 2 {
		if store, err := OpenStore(path); err == nil {
			store.Close()
			t.Fatal("invalid legacy URL accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed open changed v8", err)
		}
	}
}

func TestStorageV9Golden(t *testing.T) {
	before, err := os.ReadFile("testdata/storage-v9.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "golden.sqlite")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		store, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshotBusinessTables(t, store.db)
		if err := validateStorageDatabase(store.db); err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("v9 golden changed")
	}
}

func TestStorageV9MigrationWALRecovery(t *testing.T) {
	for _, stage := range []string{"old_commit", "uncommitted_migration", "migration_commit"} {
		t.Run(stage, func(t *testing.T) {
			db, path := migrationFixture(t)
			db.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestStorageV9MigrationCrashHelper$")
			cmd.Env = append(os.Environ(), "SWD_V9_CRASH_PATH="+path, "SWD_V9_CRASH_STAGE="+stage)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("helper: %v %s", err, out)
			}
			if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
				t.Fatal("missing WAL evidence", err)
			}
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			var name string
			if err := store.db.QueryRow("SELECT name FROM songs WHERE id=42").Scan(&name); err != nil || name != "committed before crash" {
				t.Fatal(name, err)
			}
			if err := validateStorageDatabase(store.db); err != nil {
				t.Fatal(err)
			}
			store.Close()
		})
	}
}

func TestStorageV9MigrationCrashHelper(t *testing.T) {
	path := os.Getenv("SWD_V9_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess only")
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE songs SET name='committed before crash' WHERE id=42"); err != nil {
		t.Fatal(err)
	}
	switch os.Getenv("SWD_V9_CRASH_STAGE") {
	case "uncommitted_migration":
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range storageV9MigrationSteps() {
			if _, err := tx.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	case "migration_commit":
		if err := migrateStorageV8(db); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately leave the connection and transaction unclosed.
	os.Exit(0)
}
