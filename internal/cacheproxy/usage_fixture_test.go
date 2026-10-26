package cacheproxy

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"
)

// Low-level worker tests own their connection. Fixture initialization uses the
// same explicit create operation as applications; existing files remain strict.
func fixtureUsage(t testing.TB, path string, log *slog.Logger, retentionDays int) (*usageStore, error) {
	t.Helper()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		store, err := CreateStore(path)
		if err != nil {
			return nil, err
		}
		if err := store.Close(); err != nil {
			return nil, err
		}
	}
	db, err := connectStorageDatabase(path, "rw", false)
	if err != nil {
		return nil, err
	}
	u := newUsage(db, log)
	if err := u.start(retentionDays); err != nil {
		db.Close()
		return nil, err
	}
	t.Cleanup(func() { u.close(); db.Close() })
	return u, nil
}

func cacheReadDB(root string) (*sql.DB, error) { return readDatabaseFile(storagePath(root)) }

func TestUsageWorkerBorrowsApplicationDatabase(t *testing.T) {
	store, err := CreateStore(storagePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.usage.start(0); err != nil {
		t.Fatal(err)
	}
	store.usage.record(usageEvent{songID: "42", at: 1000, demand: true})
	if err := store.usage.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Ping(); err != nil {
		t.Fatal("worker closed the application connection", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Ping(); err == nil {
		t.Fatal("application did not close its connection")
	}
}
