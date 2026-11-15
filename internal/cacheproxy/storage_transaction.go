package cacheproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func isStorageBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY || sqliteErr.Code()&0xff == sqlite3.SQLITE_LOCKED)
}

// Keep the physical connection out of the pool until the transaction is cleaned
// up. database/sql marks a Tx done even when SQLite COMMIT returns SQLITE_BUSY;
// a deferred sql.Tx.Rollback then cannot roll back the still-open SQLite tx.
type storageTransaction struct {
	*sql.Tx
	conn  *sql.Conn
	owned bool
}

func beginStorageWrite(ctx context.Context, db *sql.DB) (*storageTransaction, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := beginStorageWriteOn(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	tx.owned = true
	return tx, nil
}

// Migrations already own a connection for connection-local PRAGMAs.
func beginStorageWriteOn(ctx context.Context, conn *sql.Conn) (*storageTransaction, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &storageTransaction{Tx: tx, conn: conn}, nil
}

func (tx *storageTransaction) discard() {
	// ErrBadConn asks database/sql to close this physical connection, which
	// releases any remaining SQLite transaction. Preserve the original error.
	_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
}

func (tx *storageTransaction) Commit() error {
	err := tx.Tx.Commit()
	if err != nil {
		// Commit may also have stopped before reaching SQLite on cancellation.
		_ = tx.Tx.Rollback()
		tx.discard()
	}
	if tx.owned {
		_ = tx.conn.Close()
	}
	return err
}

func (tx *storageTransaction) Rollback() error {
	err := tx.Tx.Rollback()
	if err != nil && !errors.Is(err, sql.ErrTxDone) {
		tx.discard()
	}
	if tx.owned {
		_ = tx.conn.Close()
	}
	return err
}
