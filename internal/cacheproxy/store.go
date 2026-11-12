package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"still-wanna-dance/internal/workfiles"
)

// Store owns one existing business database and its writer lease. It has no
// dependency on video directories, configuration snapshots or download workers.
// The application closes it after every consumer has stopped.
type Store struct {
	path   string
	db     *sql.DB
	unlock func() error
	usage  *usageStore
	log    *slog.Logger

	// Every mapping/preference commit and engine eviction/publication use the
	// same locks. No post-commit callback substitutes for this exclusion.
	retentionRunMu      sync.Mutex
	mappingMu           sync.Mutex
	catalogObservations map[string]catalogObservation // Protected by mappingMu; diagnostic only.
	binding             *retentionBinding             // guarded by retentionRunMu
	mu                  sync.Mutex
	wg                  sync.WaitGroup
	closed              bool
	done                chan struct{}
	closeErr            error
	failure             error
	failed              chan struct{}
	metadataUpdated     time.Time // Committed metadata changes in this Store session; guarded by mu.

	// One shared wakeup channel coalesces changes without retaining subscribers.
	historyRevision uint64
	historyChanged  chan struct{}
}

// OpenStore never creates a database, repairs its schema or recreates a lost
// parent. Empty, incompatible and damaged files are preserved for recovery.
func OpenStore(path string) (*Store, error) { return openStore(path, false) }

// CreateStore is an explicit creation operation, also used by desktop first-run
// initialization. It exclusively creates a new file; existing main files or
// recovery companions, even empty ones, are not permission to initialize it.
func CreateStore(path string) (*Store, error) { return openStore(path, true) }

func openStore(path string, create bool) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("请选择数据库文件")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	if create {
		if err := workfiles.CheckFutureDir(parent); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(parent, 0700); err != nil {
			return nil, err
		}
	}
	if err := workfiles.CheckDir(parent); err != nil {
		return nil, err
	}
	if create {
		if err := validateDatabasePathSpelling(parent); err != nil {
			return nil, err
		}
	}
	_, err = os.Lstat(path)
	if err == nil {
		if create {
			return nil, fmt.Errorf("数据库文件已存在，不会覆盖：%w", os.ErrExist)
		}
		if err := CheckDatabaseFile(path); err != nil {
			return nil, err
		}
	} else if !create || !os.IsNotExist(err) {
		return nil, err
	}
	leasePath := databaseLeasePath(path)
	if info, err := os.Lstat(leasePath); err == nil && !workfiles.Regular(info) {
		return nil, errors.New("数据库锁不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	unlock, err := lockDirectory(leasePath)
	if err != nil {
		return nil, fmt.Errorf("数据库正被其他实例使用：%w", err)
	}
	if create {
		// Check under the writer lease before SQLite can touch recovery material.
		// A missing main file does not make its companions disposable.
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); err == nil {
				unlock()
				return nil, fmt.Errorf("数据库恢复文件已存在，请保留原文件并选择其他新建位置：%s：%w", path+suffix, os.ErrExist)
			} else if !os.IsNotExist(err) {
				unlock()
				return nil, fmt.Errorf("无法检查数据库恢复文件：%w", err)
			}
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			unlock()
			return nil, err
		}
		if err := f.Close(); err != nil {
			unlock()
			return nil, err
		}
		if err := CheckDatabaseFile(path); err != nil {
			unlock()
			return nil, err
		}
	}
	// rw cannot recreate a file lost between the filesystem check and SQLite's
	// first connection. Only the explicit branch above creates a file.
	db, err := connectStorageDatabase(path, "rw", create)
	if err != nil {
		unlock()
		// Preserve an incomplete creation too; never guess which journal files
		// are disposable after a failed SQLite transaction.
		return nil, err
	}
	return newStore(path, db, unlock), nil
}

func newStore(path string, db *sql.DB, unlock func() error) *Store {
	s := &Store{path: path, db: db, unlock: unlock, log: slog.Default(), done: make(chan struct{}), failed: make(chan struct{})}
	s.usage = newUsage(db, s.log)
	s.historyChanged = make(chan struct{})
	s.usage.onHistoryChange = s.notifyHistoryChanged
	// Lost observations are logged and reported by usage.close. Only terminal
	// storage faults disable downloads; background conflicts and timeouts use
	// the same classification as synchronous Store operations.
	s.usage.onError = s.rememberFailure
	return s
}

func databaseLeasePath(path string) string { return path + ".lock" }

func (s *Store) Close() error {
	s.retentionRunMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.retentionRunMu.Unlock()
		<-s.done
		return s.closeErr
	}
	if s.binding != nil {
		s.mu.Unlock()
		s.retentionRunMu.Unlock()
		return errors.New("请先关闭视频引擎，再关闭数据库")
	}
	s.closed = true
	close(s.historyChanged)
	s.mu.Unlock()
	s.retentionRunMu.Unlock()
	s.wg.Wait()
	usageErr := s.usage.close()
	// Synchronous operation failures were already returned to their callers.
	// Close additionally reports work the background collector could not save.
	s.closeErr = errors.Join(usageErr, s.db.Close(), s.unlock())
	close(s.done)
	return s.closeErr
}

func (s *Store) beginRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *Store) attach(binding *retentionBinding) error {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("数据库已关闭")
	}
	if s.binding != nil {
		return errors.New("数据库已被另一个视频引擎使用")
	}
	s.binding = binding
	return nil
}

func (s *Store) detach(binding *retentionBinding) {
	s.retentionRunMu.Lock()
	if s.binding == binding {
		s.binding = nil
	}
	s.retentionRunMu.Unlock()
}

// Caller holds retentionRunMu through the commit and its invalidation.
func (s *Store) changed(queue bool) {
	if s.binding != nil {
		s.binding.changed(queue)
	}
}

// MetadataUpdatedAt reports actual committed catalog/mapping changes, not
// repeated observations or successful checks. It never reads the database.
func (s *Store) MetadataUpdatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadataUpdated
}

// A binding is an engine's synchronous invalidation lease. Store knows neither
// the engine's concrete type nor its locks. Detach waits for in-flight commits.
type retentionBinding struct{ changed func(queue bool) }

// A decision includes its database snapshot, filesystem publication/deletion,
// and settlement. Mapping/preference writes cannot interleave with that scope.
func (s *Store) retentionDecision(decide func() error) error {
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	return decide()
}

// Error reports a database I/O failure independently of any video engine.
// Business conflicts, rejected samples and cancellation are not corruption.
func (s *Store) Error() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *Store) rememberFailure(err error) {
	if isDatabaseStorageError(err) {
		s.retainFailure(fmt.Errorf("数据库读写失败：%w: %w", ErrLocalStorage, err))
	}
}

func isDatabaseStorageError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var databaseErr *sqlite.Error
	var pathErr *os.PathError
	fault := errors.As(err, &pathErr)
	if errors.As(err, &databaseErr) {
		switch databaseErr.Code() & 0xff {
		case sqlite3.SQLITE_IOERR, sqlite3.SQLITE_FULL, sqlite3.SQLITE_READONLY, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB, sqlite3.SQLITE_CANTOPEN, sqlite3.SQLITE_PROTOCOL:
			fault = true
		}
	}
	return fault
}

func (s *Store) retainFailure(err error) {
	s.mu.Lock()
	if s.failure == nil {
		s.failure = err
		if s.failed != nil {
			close(s.failed)
		}
	}
	s.mu.Unlock()
}

// Waiting engine work can observe failure without a callback acquiring engine
// locks from the statistics worker (whose flush callers may hold those locks).
func (s *Store) failureDone() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.failed
}

// FailureDone closes when the store retains its first terminal I/O failure.
func (s *Store) FailureDone() <-chan struct{} { return s.failureDone() }

func (s *Store) storageError(err error) error {
	s.rememberFailure(err)
	return err
}
