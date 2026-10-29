package cacheproxy

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCreationPreservesRecoveryFiles(t *testing.T) {
	seed := filepath.Join(t.TempDir(), "seed.sqlite")
	store, err := CreateStore(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", seed)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA cache_size=1; INSERT INTO songs(id,name) VALUES(7,'original')"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE songs SET name='pending' WHERE id=7; WITH RECURSIVE n(x) AS (VALUES(100) UNION ALL SELECT x+1 FROM n WHERE x<3000) INSERT INTO songs(id,name) SELECT x, printf('%01000d',x) FROM n"); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(seed + "-journal")
	if err != nil || len(journal) == 0 {
		t.Fatal("missing real SQLite journal", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, suffix string
		data         []byte
		directory    bool
	}{
		{"journal", "-journal", journal, false},
		{"wal", "-wal", []byte("existing WAL evidence"), false},
		{"shm", "-shm", []byte("existing SHM evidence"), false},
		{"empty-journal", "-journal", nil, false},
		{"directory", "-wal", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "personal.sqlite")
			if tc.directory {
				err = os.Mkdir(path+tc.suffix, 0700)
			} else {
				err = os.WriteFile(path+tc.suffix, tc.data, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			created, createErr := CreateStore(path)
			if created != nil {
				created.Close()
			}
			if createErr == nil {
				t.Error("creation accepted existing recovery material")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Error("creation left a new main file", err)
			}
			if tc.directory {
				if info, err := os.Lstat(path + tc.suffix); err != nil || !info.IsDir() {
					t.Error("changed existing directory", err)
				}
			} else if after, err := os.ReadFile(path + tc.suffix); err != nil || !bytes.Equal(after, tc.data) {
				t.Error("changed recovery material", err)
			}
			release, err := lockDirectory(databaseLeasePath(path))
			if err != nil {
				t.Fatal("failure retained database lease", err)
			}
			release()
		})
	}
}

func TestStoreOpenDoesNotCreateOrRepair(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing", "library.sqlite")
	if s, err := OpenStore(missing); s != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database: %v, %v", s, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("open recreated a parent", err)
	}
	for _, content := range [][]byte{nil, []byte("not a database")} {
		path := filepath.Join(t.TempDir(), "library.sqlite")
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if s, err := OpenStore(path); s != nil || err == nil {
			t.Fatal("accepted invalid database", err)
		}
		if s, err := CreateStore(path); s != nil || !errors.Is(err, os.ErrExist) {
			t.Fatal("replaced existing file", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, content) {
			t.Fatal("modified invalid database", err)
		}
	}
}

func TestStorePreservesV6WithoutVideoOrObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database", "personal.sqlite")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStorageDatabase(s.db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO songs(id) VALUES(17); INSERT INTO song_preferences VALUES(17,'favorite')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM request_observation").Scan(&count); err != nil || count != 0 {
		t.Fatal("opening the database started request observation", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("repeat close", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatal("database created a video or work directory", entry.Name())
		}
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var policy string
	if err := s.db.QueryRow("SELECT policy FROM song_preferences WHERE song_id=17").Scan(&policy); err != nil || policy != "favorite" {
		t.Fatal("lost personal data", policy, err)
	}
}

func TestStoreLeaseExcludesOtherWriters(t *testing.T) {
	for _, name := range []string{"storage.sqlite", "personal.sqlite"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			s, err := CreateStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if other, err := OpenStore(path); err == nil {
				other.Close()
				t.Fatal("second writer acquired database")
			}
			if release, err := lockDirectory(path + ".lock"); err == nil {
				release()
				t.Fatal("database lease was not held")
			}
			sibling, err := CreateStore(filepath.Join(filepath.Dir(path), "independent.sqlite"))
			if err != nil {
				t.Fatal("independent database was blocked", err)
			}
			if err := sibling.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			other, err := OpenStore(path)
			if err != nil {
				t.Fatal("lease was not released", err)
			}
			other.Close()
		})
	}
}

func TestStoreRejectsHardLinkedDatabasesWithoutChangingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.sqlite")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSongPolicy(t.Context(), 17, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(path), "same-library.sqlite")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, alias} {
		if opened, err := OpenStore(name); err == nil {
			opened.Close()
			t.Error("opened a database with multiple hard-link names", name)
		}
		if db, err := readDatabaseFile(name); err == nil {
			db.Close()
			t.Error("read a database through unsupported aliases", name)
		}
		if after, err := os.ReadFile(name); err != nil || !bytes.Equal(before, after) {
			t.Fatal("changed linked database", err)
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal("normal database did not reopen", err)
	}
	defer s.Close()
	policies, err := s.SongPolicies(t.Context())
	if err != nil || policies[17] != SongFavorite {
		t.Fatal("lost saved preference", policies, err)
	}
}

func TestStoreRejectsHardLinkAddedWhileWriterOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.sqlite")
	s, err := CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alias := filepath.Join(filepath.Dir(path), "same-library.sqlite")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenStore(alias); err == nil {
		other.Close()
		t.Fatal("alias bypassed active database ownership")
	}
	if err := s.SetSongPolicy(t.Context(), 17, SongFavorite); err != nil {
		t.Fatal("existing owner lost access", err)
	}
}

func TestStoreOpenRejectsUnsupportedV6ChangesWithoutWriting(t *testing.T) {
	t.Run("v2 fixture", func(t *testing.T) {
		path := storagePath(t.TempDir())
		original, err := os.ReadFile(filepath.Join("testdata", "storage-v2.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		if store, err := OpenStore(path); err == nil {
			store.Close()
			t.Fatal("opened schema v2")
		}
		if after, err := os.ReadFile(path); err != nil || !bytes.Equal(original, after) {
			t.Fatal("open changed old database", err)
		}
	})
	for _, mutation := range []string{"PRAGMA user_version=4", "PRAGMA user_version=999", "DROP TABLE catalog_checks"} {
		t.Run(mutation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "library.sqlite")
			s, err := CreateStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if opened, err := OpenStore(path); err == nil {
				opened.Close()
				t.Fatalf("accepted %s", mutation)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("repaired or changed unsupported database", err)
			}
		})
	}
}
