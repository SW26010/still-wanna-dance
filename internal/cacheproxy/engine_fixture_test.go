package cacheproxy

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Fixture setup is an explicit application operation; New never creates either
// resource. The sibling layout is convenient for existing metadata fixtures.
func prepareFixtureResources(t testing.TB, cfg *Config, root string, create bool) {
	t.Helper()
	var err error
	if create {
		cfg.Store, err = CreateStore(storagePath(root))
	} else {
		cfg.Store, err = OpenStore(storagePath(root))
	}
	if err != nil {
		t.Fatal(err)
	}
	store := cfg.Store
	t.Cleanup(func() {
		if engine := store.engine.Load(); engine != nil {
			engine.Close()
		}
		store.Close()
	})
	prepareFixtureVideo(t, cfg, filepath.Join(root, "videos"))
}

func prepareFixtureVideo(t testing.TB, cfg *Config, videoDir string) {
	t.Helper()
	cfg.VideoDir = videoDir
	var err error
	if err := os.MkdirAll(cfg.VideoDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Work, err = OpenWorkDirectory(t.TempDir(), cfg.Logger)
	if err != nil {
		t.Fatal(err)
	}
	work := cfg.Work
	t.Cleanup(func() {
		if cfg.Store != nil {
			if engine := cfg.Store.engine.Load(); engine != nil {
				engine.Close()
			}
		}
		work.Close()
	})
}

func storagePath(root string) string { return filepath.Join(root, storageDatabaseName) }

// Low-level SQL contract fixtures explicitly request initialization. Runtime
// ownership and strict opens are covered by Store tests, not by this helper.
func openFixtureDatabase(path string, writable bool) (*sql.DB, error) {
	if writable {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			store, err := CreateStore(path)
			if err != nil {
				return nil, err
			}
			if err = store.Close(); err != nil {
				return nil, err
			}
		}
		return connectStorageDatabase(path, "rw", false)
	}
	return readDatabaseFile(path)
}

// Reopen the application database to verify persistence across connection lifetimes.
func reopenFixtureStore(t testing.TB, cfg *Config) {
	t.Helper()
	path := cfg.Store.path
	if err := cfg.Store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	cfg.Store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store := cfg.Store
	t.Cleanup(func() {
		if engine := store.engine.Load(); engine != nil {
			engine.Close()
		}
		store.Close()
	})
}

// Keep the fixture's lease in its parent so failure tests can replace the block
// target itself with a file without releasing ownership or deleting a lock.
func fixtureFaultableBlocks(t *testing.T, cfg *Config) {
	t.Helper()
	dir := filepath.Join(cfg.Work.Path(), "blocks")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Work.path = dir
}
