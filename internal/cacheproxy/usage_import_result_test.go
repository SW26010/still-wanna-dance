package cacheproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"modernc.org/sqlite"
)

// Pause only after SQLite has committed, before database/sql returns the result
// to the usage worker. Cancellation at this boundary cannot undo the commit.
type importCommitConnector struct {
	path               string
	cancel             context.CancelFunc
	committed, release chan struct{}
	once               sync.Once
}

func (c *importCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *importCommitConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(storageDatabaseURI(c.path, "rw"))
	if err != nil {
		return nil, err
	}
	return &importCommitConn{Conn: conn, pause: c}, nil
}

type importCommitConn struct {
	driver.Conn
	pause *importCommitConnector
}

func (c *importCommitConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &importCommitTx{Tx: tx, pause: c.pause}, nil
}

type importCommitTx struct {
	driver.Tx
	pause *importCommitConnector
}

func (tx *importCommitTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil {
		tx.pause.once.Do(func() {
			tx.pause.cancel()
			close(tx.pause.committed)
			<-tx.pause.release
		})
	}
	return err
}

func TestHistoricalUsageCommittedResultSurvivesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := createEmptyFixtureStore(storagePath(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		pause := &importCommitConnector{path: s.path, cancel: cancel, committed: make(chan struct{}), release: make(chan struct{})}
		db := sql.OpenDB(pause)
		db.SetMaxOpenConns(1)
		s.db, s.usage.db = db, db
		done := make(chan error, 1)
		go func() { done <- s.ImportSongUsage(ctx, historyAt(42, time.Now().UnixMilli()), true) }()
		select {
		case <-pause.committed:
		case err := <-done:
			t.Fatal("import ended before reaching the commit boundary:", err)
		}
		var count int
		if err := s.playbackDB.QueryRow("SELECT count(*) FROM song_usage WHERE song_id=42").Scan(&count); err != nil || count != 1 {
			close(pause.release)
			t.Fatal("SQLite did not commit before cancellation:", count, err)
		}
		synctest.Wait()
		returned := false
		select {
		case err := <-done:
			returned = true
			t.Errorf("import returned before the committed worker result: %v", err)
		default:
		}
		close(pause.release)
		if !returned {
			if err := <-done; err != nil {
				t.Fatal("cancellation replaced a successful commit result:", err)
			}
		}
	})
}
