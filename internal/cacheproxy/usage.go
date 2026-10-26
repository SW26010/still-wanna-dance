package cacheproxy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Counts describe GET demand or explicitly imported historical observations,
// not confirmed playback. Deduplication is anchored at the last counted demand.
const demandWindow = 30 * time.Second

const requestCleanupBatch = 500

type usageEvent struct {
	songID                                         string
	firstBodyNS                                    int64
	responseBytes                                  *int64
	firstBodyBlocking                              *bool
	completedAt                                    int64
	barrier                                        chan struct{}
	at                                             int64
	key, host, method, rangeHeader, cache, outcome string
	size, bytes, elapsedMS                         int64
	status                                         int
	summaryOnly                                    bool
}

type usageStore struct {
	retention time.Duration
	db        *sql.DB
	log       *slog.Logger
	mu        sync.Mutex
	closed    bool
	started   bool
	onError   func(error)
	failure   error // Written by the worker; read after done closes.
	events    chan usageEvent
	done      chan struct{}
	now       func() time.Time
}

func newUsage(db *sql.DB, log *slog.Logger) *usageStore {
	return &usageStore{db: db, log: log, events: make(chan usageEvent, 1024), done: make(chan struct{}), now: time.Now}
}

// Opening a library is not the beginning of request collection. Only an engine
// that actually starts observing traffic activates this worker and its pruning.
func (u *usageStore) start(retentionDays int) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return fmt.Errorf("request observation is closed")
	}
	if u.started {
		return nil
	}
	if _, err := u.db.Exec("INSERT INTO request_observation(id,started_at,retained_since) VALUES(1,?,0) ON CONFLICT DO NOTHING", u.now().UnixMilli()); err != nil {
		return err
	}
	u.retention = time.Duration(retentionDays) * 24 * time.Hour
	u.started = true
	go u.run()
	return nil
}

// Timestamp and enqueue under the same lock: response completion cannot reorder
// demand observations. The database worker remains off the HTTP request path.
func (u *usageStore) startGET(id, key string) int64 {
	if u == nil {
		return time.Now().UnixMilli()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	at := u.now().UnixMilli()
	u.enqueueLocked(usageEvent{songID: id, key: key, at: at, summaryOnly: true})
	return at
}

func (u *usageStore) record(e usageEvent) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	// Completion observations and their enqueue order share one boundary.
	// Request start timestamps remain unchanged for demand/usage history.
	e.completedAt = u.now().UnixMilli()
	u.enqueueLocked(e)
}

// Drain observations already accepted before ranking eviction candidates.
func (u *usageStore) flush() {
	if u == nil {
		return
	}
	u.mu.Lock()
	if u.closed || !u.started {
		u.mu.Unlock()
		return
	}
	done := make(chan struct{})
	u.events <- usageEvent{barrier: done}
	u.mu.Unlock()
	<-done
}

func (u *usageStore) enqueueLocked(e usageEvent) {
	if u.closed || !u.started {
		return
	}
	select {
	case u.events <- e:
	default:
		u.log.Warn("usage_dropped", "resource_key", e.key, "song_id", e.songID)
	}
}

func (u *usageStore) close() error {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	if !u.closed {
		u.closed = true
		if u.started {
			close(u.events)
		} else {
			close(u.done)
		}
	}
	u.mu.Unlock()
	<-u.done
	return u.failure
}

func (u *usageStore) run() {
	defer close(u.done)
	// Run even without requests; catch up old databases gradually.
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		var event usageEvent
		select {
		case <-timer.C:
			deleted, err := u.prune(time.Now())
			delay := time.Minute
			if err != nil {
				if u.failure == nil {
					u.failure = err
				}
				u.log.Warn("usage_cleanup_failed", "error", err)
				if u.onError != nil {
					u.onError(err)
				}
			} else if deleted == requestCleanupBatch {
				delay = time.Second
			}
			timer.Reset(delay)
			continue
		case e, ok := <-u.events:
			if !ok {
				return
			}
			event = e
		}
		batch := []usageEvent{event}
		// Drain an immediately available batch without delaying the request path.
		for len(batch) < 128 {
			select {
			case e, ok := <-u.events:
				if !ok {
					goto write
				}
				batch = append(batch, e)
			default:
				goto write
			}
		}
	write:
		if err := u.write(batch); err != nil {
			if u.failure == nil {
				u.failure = err
			}
			u.log.Error("usage_write_failed", "count", len(batch), "error", err)
			if u.onError != nil {
				u.onError(err)
			}
		}
		for _, e := range batch {
			if e.barrier != nil {
				close(e.barrier)
			}
		}
	}
}

func (u *usageStore) write(batch []usageEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range batch {
		if e.barrier != nil {
			continue
		}
		var id int64
		if e.songID != "" {
			var parseErr error
			id, parseErr = strconv.ParseInt(e.songID, 10, 64)
			if parseErr != nil || id <= 0 {
				return fmt.Errorf("invalid request song ID: %q", e.songID)
			}
		}
		if !e.summaryOnly {
			if err := recordPlaybackTransfer(ctx, tx, e, id); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO request_events
(resource_md5, requested_at, host, method, range_header, cache_result, outcome,
 file_bytes, transferred_bytes, elapsed_ms, status, song_id, completed_at, response_bytes, first_body_ns, first_body_blocking)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				nullString(e.key), e.at, e.host, e.method, e.rangeHeader, e.cache, e.outcome,
				e.size, e.bytes, e.elapsedMS, e.status, nullPositive(id), nullPositive(e.completedAt), e.responseBytes, nullFirstBody(e), e.firstBodyBlocking)
			if err != nil {
				return err
			}
			continue
		}
		// Access history supports the cache list, independently of song scoring
		// and the retention period for request details.
		if e.key != "" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO media_access(md5,last_requested_at) VALUES (?,?)
 ON CONFLICT(md5) DO UPDATE SET last_requested_at=max(last_requested_at,excluded.last_requested_at)`, e.key, e.at); err != nil {
				return err
			}
		}
		if id == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO songs(song_id) VALUES (?) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
		var score float64
		var last int64
		err = tx.QueryRowContext(ctx, "SELECT demand_score,last_demand_at FROM song_usage WHERE song_id=?", id).Scan(&score, &last)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		score, counted := countedDemand(score, last, e.at, err == nil)
		if !counted {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO song_usage(song_id,demand_count,demand_score,last_demand_at)
          VALUES (?,1,?,?) ON CONFLICT(song_id) DO UPDATE SET demand_count=demand_count+1,
          demand_score=excluded.demand_score,last_demand_at=excluded.last_demand_at`, id, score, e.at)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete only details in a bounded transaction using the time index.
// SQLite reuses freed pages; avoid a blocking full VACUUM during service use.
func (u *usageStore) prune(now time.Time) (int64, error) {
	if u.retention == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cutoff := now.Add(-u.retention).UnixMilli()
	if _, err := tx.ExecContext(ctx, "UPDATE request_observation SET retained_since=max(retained_since,?) WHERE id=1", cutoff); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM request_events WHERE event_id IN (
 SELECT event_id FROM request_events WHERE requested_at < ? ORDER BY requested_at LIMIT ?
)`, cutoff, requestCleanupBatch)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullPositive(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}
func nullFirstBody(e usageEvent) any {
	if e.firstBodyBlocking == nil || e.bytes <= 0 {
		return nil
	}
	return e.firstBodyNS
}
