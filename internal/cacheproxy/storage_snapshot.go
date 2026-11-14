package cacheproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

// Copy through SQLite, including committed WAL contents. Each backup step holds
// the source read lock for at most 128 pages; long scans/output only read the copy.
// The caller owns cleanup, including partially copied databases on failure.
func snapshotStorageDatabase(ctx context.Context, path, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := readDatabaseFile(path)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(raw any) (resultErr error) {
		source, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite adapter does not support online backup")
		}
		backup, err := source.NewBackup(storageDatabaseURI(destination, "rwc"))
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	})
}

func temporaryStorageSnapshot(ctx context.Context, path string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "swd-storage-snapshot-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	snapshot := filepath.Join(dir, "snapshot.sqlite")
	if err := snapshotStorageDatabase(ctx, path, snapshot); err != nil {
		cleanup()
		return "", nil, err
	}
	return snapshot, cleanup, nil
}
