package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"time"
)

// Verification data is separate from catalog/usage data so a scanner need not
// start the cache engine or mutate song associations. Missing records are safe:
// the next use hashes the file again. All fingerprints describe opened files.
type verificationStore struct {
	db   *sql.DB
	root string
}

// Bound lock storage while coalescing checks of a resource across scanners and
// playback in this process. SQLite handles publication across processes.
type checkLocks [64]chan struct{}

func newCheckLocks() checkLocks {
	var locks checkLocks
	for i := range locks {
		locks[i] = make(chan struct{}, 1)
	}
	return locks
}

func (locks *checkLocks) acquire(ctx context.Context, key string) (func(), error) {
	slot := locks[crc32.ChecksumIEEE([]byte(key))%uint32(len(locks))]
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var verificationLocks = newCheckLocks()

func openVerificationStore(root string) (*verificationStore, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "verification.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS verified_files (
 path TEXT PRIMARY KEY, version_key TEXT NOT NULL, identity TEXT NOT NULL,
 file_bytes INTEGER NOT NULL, modified_ns INTEGER NOT NULL, mode INTEGER NOT NULL,
 checked_ns INTEGER NOT NULL
);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &verificationStore{db: db, root: root}, nil
}

func (s *verificationStore) validate(ctx context.Context, f *verifiedFile, v video, force bool) (bool, error) {
	unlock, err := verificationLocks.acquire(ctx, s.root+v.key)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	before, err := f.file.Stat()
	if err != nil {
		return false, err
	}
	identity, err := fileIdentity(f.file)
	if err != nil {
		return false, err
	}
	path := filepath.Join(s.root, "videos", v.key+".mp4")
	var key, savedID string
	var size, modified, mode, checked int64
	err = s.db.QueryRowContext(ctx, `SELECT version_key, identity, file_bytes, modified_ns, mode, checked_ns FROM verified_files WHERE path=?`, path).Scan(&key, &savedID, &size, &modified, &mode, &checked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if !force && err == nil && identity != "" && key == v.key && savedID == identity &&
		before.Mode().IsRegular() && before.Size() == v.size && size == before.Size() &&
		modified == before.ModTime().UnixNano() && mode == int64(before.Mode()) {
		f.info, f.checkedAt = before, time.Unix(0, checked)
		return true, ctx.Err()
	}
	// Invalidate before reading: failed/canceled checks must never leave a
	// previously trusted fingerprint available for a later fast path.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM verified_files WHERE path=?`, path); err != nil {
		return false, err
	}
	if err := checkOpenFile(ctx, f.file, v); err != nil {
		return false, err
	}
	after, err := f.file.Stat()
	if err != nil {
		return false, err
	}
	if !sameVerifiedFile(before, after) {
		return false, fmt.Errorf("%w: cached file changed during validation", errInvalidCache)
	}
	f.info, f.checkedAt = after, time.Now()
	if err := s.remember(ctx, v, identity, after, f.checkedAt); err != nil {
		return false, err
	}
	return false, nil
}

func (s *verificationStore) remember(ctx context.Context, v video, identity string, info os.FileInfo, checked time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO verified_files(path, version_key, identity, file_bytes, modified_ns, mode, checked_ns)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(path) DO UPDATE SET
 version_key=excluded.version_key, identity=excluded.identity, file_bytes=excluded.file_bytes,
 modified_ns=excluded.modified_ns, mode=excluded.mode, checked_ns=excluded.checked_ns`,
		filepath.Join(s.root, "videos", v.key+".mp4"), v.key, identity, info.Size(), info.ModTime().UnixNano(), int64(info.Mode()), checked.UnixNano())
	return err
}

func (s *verificationStore) forget(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM verified_files WHERE path=?`, filepath.Join(s.root, "videos", key+".mp4"))
	return err
}
