package cacheproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestStorageFailedCommitReleasesTransaction(t *testing.T) {
	for _, operation := range []string{"favorite", "history"} {
		t.Run(operation, func(t *testing.T) {
			s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.usage.unlimited = true
			// An actual rollback-journal reader blocks COMMIT, not the preceding writes.
			reader, err := readDatabaseFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			tx, err := reader.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var count int
			if err := tx.QueryRow("SELECT count(*) FROM songs").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("PRAGMA busy_timeout=0"); err != nil {
				t.Fatal(err)
			}
			if operation == "favorite" {
				err = s.SetSongPolicy(t.Context(), 42, SongFavorite)
			} else {
				err = s.usage.write([]usageEvent{{at: 999, method: "GET"}})
			}
			var sqliteErr *sqlite.Error
			if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
				t.Fatalf("expected busy COMMIT: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			policies, err := s.SongPolicies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if policies[42] == SongFavorite {
				t.Error("same session sees the rejected, uncommitted favorite")
			}
			if err := s.db.QueryRow("SELECT count(*) FROM request_events WHERE requested_at=999").Scan(&count); err != nil || count != 0 {
				t.Errorf("rejected history in same session: %d, %v", count, err)
			}
			if err := s.usage.write([]usageEvent{{at: 1000, method: "GET", outcome: "completed"}}); err != nil {
				t.Errorf("later history write: %v", err)
			}
			if err := s.SetSongPolicy(t.Context(), 43, SongFavorite); err != nil {
				t.Errorf("later favorite write: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(s.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			policies, err = reopened.SongPolicies(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if policies[42] == SongFavorite || policies[43] != SongFavorite {
				t.Errorf("reopened preferences: %v", policies)
			}
			if err := reopened.db.QueryRow("SELECT count(*) FROM request_events WHERE requested_at=1000").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("reopened history rows=%d", count)
			}
		})
	}
}

func TestStorageCanceledWriteReleasesTransaction(t *testing.T) {
	s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	func() {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tx, err := beginStorageWrite(ctx, s.db)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := writeSongPolicy(ctx, tx.Tx, 42, SongFavorite); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := tx.Commit(); !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("canceled commit: %v", err)
		}
	}()
	policies, err := s.SongPolicies(t.Context())
	if err != nil || policies[42] == SongFavorite {
		t.Fatal(policies, err)
	}
	if err := s.SetSongPolicy(t.Context(), 43, SongFavorite); err != nil {
		t.Fatal("write after cancellation", err)
	}
}

// Inject only a failed rollback over a real SQLite transaction. The raw
// connection would otherwise return to the pool with its writes still visible.
type rollbackFaultConnector struct {
	path string
	fail bool
}

func (c *rollbackFaultConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *rollbackFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(storageDatabaseURI(c.path, "rw"))
	if err != nil {
		return nil, err
	}
	return &rollbackFaultConn{Conn: conn, fault: c}, nil
}

type rollbackFaultConn struct {
	driver.Conn
	fault *rollbackFaultConnector
}

func (c *rollbackFaultConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &rollbackFaultTx{Tx: tx, fault: c.fault}, nil
}

type rollbackFaultTx struct {
	driver.Tx
	fault *rollbackFaultConnector
}

var errRollbackFault = errors.New("injected SQLite rollback failure")

func (tx *rollbackFaultTx) Rollback() error {
	if tx.fault.fail {
		tx.fault.fail = false
		return errRollbackFault
	}
	return tx.Tx.Rollback()
}

func TestStorageFailedRollbackDiscardsConnection(t *testing.T) {
	s, err := createEmptyFixtureStore(filepath.Join(t.TempDir(), "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&rollbackFaultConnector{path: s.path, fail: true})
	db.SetMaxOpenConns(1)
	defer db.Close()
	tx, err := beginStorageWrite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSongPolicy(t.Context(), tx.Tx, 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); !errors.Is(err, errRollbackFault) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM song_preferences").Scan(&count); err != nil || count != 0 {
		t.Fatal("uncommitted data survived rollback failure", count, err)
	}
	next, err := beginStorageWrite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Rollback()
	if err := writeSongPolicy(t.Context(), next.Tx, 43, SongFavorite); err != nil {
		t.Fatal(err)
	}
	if err := next.Commit(); err != nil {
		t.Fatal(err)
	}
}
