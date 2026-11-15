package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

const historyCopyInterval = 5 * time.Second

// RequestHistory owns read isolation and query plans for one database. Store
// owns its lifetime; offline callers can create and close a standalone reader.
// Time pages stay live and bounded. All potentially unbounded work uses a
// shared private copy, never a long read transaction on the business database.
type RequestHistory struct {
	path        string
	owned       bool  // Store's exclusive writer owns detail invalidation.
	revision    int64 // Committed details, guarded by mu; independent of GET-only SSE.
	mu          sync.Mutex
	source      *sql.DB   // Only PRAGMA data_version; never used for history scans.
	versionConn *sql.Conn // data_version is comparable only on this physical connection.
	current     *historySnapshot
	building    *historyBuild
	nextCopy    time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
}

type historySnapshot struct {
	db           *sql.DB
	path         string
	cleanup      func()
	version      int64
	at           int64
	readers      int
	indexGate    chan struct{}
	latencyIndex historyCache[bool]
	sources      historyCache[ResponseSources]
	latency      historyCache[historyLatency]
}

type historyBuild struct {
	done chan struct{}
	err  error
}

// NewRequestHistory is inert until read. Callers must Close it; applications
// normally use Store.History instead of constructing a reader per request.
func NewRequestHistory(path string) *RequestHistory {
	ctx, cancel := context.WithCancel(context.Background())
	return &RequestHistory{path: path, ctx: ctx, cancel: cancel}
}

func (h *RequestHistory) invalidate() {
	h.mu.Lock()
	h.revision++
	h.mu.Unlock()
}

// Close cancels pending copies and releases the cached generation. Existing
// readers own leases and remove their copy after their last query/output ends.
func (h *RequestHistory) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.cancel()
	source, versionConn, snapshot := h.source, h.versionConn, h.current
	h.source, h.versionConn, h.current = nil, nil, nil
	dispose := snapshot != nil && snapshot.readers == 0
	h.mu.Unlock()
	if versionConn != nil {
		_ = versionConn.Close()
	}
	if source != nil {
		_ = source.Close()
	}
	if dispose {
		snapshot.close()
	}
}

func (s *historySnapshot) close() { _ = s.db.Close(); s.cleanup() }

func (h *RequestHistory) leaseLocked(s *historySnapshot) (*historySnapshot, func()) {
	s.readers++
	var once sync.Once
	return s, func() {
		once.Do(func() {
			h.mu.Lock()
			s.readers--
			dispose := s.readers == 0 && h.current != s
			h.mu.Unlock()
			if dispose {
				s.close()
			}
		})
	}
}

func (h *RequestHistory) snapshot(ctx context.Context) (*historySnapshot, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil, nil, errors.New("request history is closed")
		}
		if h.building != nil {
			build := h.building
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-build.done:
				// Cancellation belongs to the caller that led this build. Retry
				// acquisition under this waiter's own context and copy interval.
				if build.err != nil && !errors.Is(build.err, context.Canceled) && !errors.Is(build.err, context.DeadlineExceeded) {
					return nil, nil, build.err
				}
			}
			continue
		}
		if err := CheckDatabaseFile(h.path); err != nil {
			h.mu.Unlock()
			return nil, nil, err
		}
		var version int64
		if h.owned {
			version = h.revision
		} else {
			if h.source == nil {
				var err error
				h.source, err = readDatabaseFile(h.path)
				if err != nil {
					h.mu.Unlock()
					return nil, nil, err
				}
				h.versionConn, err = h.source.Conn(ctx)
				if err != nil {
					_ = h.source.Close()
					h.source = nil
					h.mu.Unlock()
					return nil, nil, err
				}
			}
			if err := h.versionConn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
				// A replacement connection restarts its data_version counter. Drop
				// the cached generation before reconnecting, even after cancellation.
				source, conn, old := h.source, h.versionConn, h.current
				h.source, h.versionConn, h.current = nil, nil, nil
				dispose := old != nil && old.readers == 0
				h.mu.Unlock()
				_ = conn.Close()
				_ = source.Close()
				if dispose {
					old.close()
				}
				return nil, nil, err
			}
		}
		if h.current != nil && h.current.version == version {
			snapshot, release := h.leaseLocked(h.current)
			h.mu.Unlock()
			return snapshot, release, nil
		}
		if delay := time.Until(h.nextCopy); delay > 0 {
			h.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		build := &historyBuild{done: make(chan struct{})}
		h.building = build
		h.nextCopy = time.Now().Add(historyCopyInterval)
		h.mu.Unlock()
		snapshot, err := buildHistorySnapshot(ctx, h.path, version)
		h.mu.Lock()
		if h.closed && err == nil {
			err = errors.New("request history is closed")
		}
		old := h.current
		if err == nil {
			h.current = snapshot
		}
		build.err = err
		h.building = nil
		close(build.done)
		disposeOld := err == nil && old != nil && old.readers == 0
		if err != nil {
			h.mu.Unlock()
			if snapshot != nil {
				snapshot.close()
			}
			return nil, nil, err
		}
		snapshot, release := h.leaseLocked(snapshot)
		h.mu.Unlock()
		if disposeOld {
			old.close()
		}
		return snapshot, release, nil
	}
}

func buildHistorySnapshot(ctx context.Context, path string, version int64) (*historySnapshot, error) {
	at := time.Now().UnixMilli()
	path, cleanup, err := temporaryStorageSnapshot(ctx, path)
	if err != nil {
		return nil, err
	}
	writer, err := connectStorageDatabase(path, "rw", false)
	if err != nil {
		cleanup()
		return nil, err
	}
	// Private WAL lets on-demand index preparation coexist with leased reads.
	// Never change the business database's journal mode or schema.
	_, err = writer.ExecContext(ctx, "PRAGMA journal_mode=WAL")
	err = errors.Join(err, writer.Close())
	if err != nil {
		cleanup()
		return nil, err
	}
	// The source and copied schema were validated above. Derived indexes are
	// prepared only by operations that need them, using this owned path.
	db, err := sql.Open("sqlite", storageDatabaseURI(path, "ro"))
	if err != nil {
		cleanup()
		return nil, err
	}
	db.SetMaxOpenConns(4)
	return &historySnapshot{db: db, path: path, cleanup: cleanup, version: version, at: at, indexGate: make(chan struct{}, 1)}, nil
}

func withHistorySnapshot[T any](ctx context.Context, h *RequestHistory, read func(context.Context, *historySnapshot) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot, release, err := h.snapshot(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	defer release()
	return read(ctx, snapshot)
}

func (h *RequestHistory) Page(ctx context.Context, query RequestQuery) (RecentRequests, error) {
	if err := h.ctx.Err(); err != nil {
		return RecentRequests{Requests: []RecentRequest{}}, err
	}
	if err := query.Validate(); err != nil {
		return RecentRequests{Requests: []RecentRequest{}}, err
	}
	return readRequestPage(ctx, h.path, query, true)
}

func (h *RequestHistory) Sources(ctx context.Context) (ResponseSources, error) {
	return withHistorySnapshot(ctx, h, func(ctx context.Context, s *historySnapshot) (ResponseSources, error) {
		return s.sources.get(ctx, func(ResponseSources) bool { return true }, func() (ResponseSources, error) { return readResponseSources(ctx, s.db) })
	})
}

func (h *RequestHistory) Latency(ctx context.Context, now time.Time) (FirstBodyLatency, error) {
	return withHistorySnapshot(ctx, h, func(ctx context.Context, s *historySnapshot) (FirstBodyLatency, error) {
		cached, err := s.latency.get(ctx, func(v historyLatency) bool { return v.contains(now) }, func() (historyLatency, error) {
			if err := s.prepareIndex(ctx, &s.latencyIndex, historyLatencyIndex); err != nil {
				return historyLatency{}, err
			}
			return readHistoryLatency(ctx, s.db, now)
		})
		return cached.result(now), err
	})
}
