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

// Counts describe observed GET demand, not confirmed playback. Deduplication is
// per song across hosts, anchored at the previous counted demand.
const demandWindow = 30 * time.Second

const requestCleanupBatch = 500

type usageEvent struct {
	songID                                                 string
	firstBodyNS                                            int64
	completedAt                                            int64
	barrier                                                chan struct{}
	id                                                     string
	at                                                     int64
	key, host, source, method, rangeHeader, cache, outcome string
	size, bytes, elapsedMS                                 int64
	status                                                 int
	demand                                                 bool
	summaryOnly                                            bool
}

type usageStore struct {
	retention time.Duration
	db        *sql.DB
	log       *slog.Logger
	mu        sync.Mutex
	closed    bool
	events    chan usageEvent
	done      chan struct{}
	now       func() time.Time
}

func openUsage(path string, log *slog.Logger, retentionDays int) (*usageStore, error) {
	db, err := openStorageDatabase(path, true)
	if err != nil {
		return nil, err
	}
	u := &usageStore{retention: time.Duration(retentionDays) * 24 * time.Hour, db: db, log: log, events: make(chan usageEvent, 1024), done: make(chan struct{}), now: time.Now}

	go u.run()
	return u, nil
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
	u.enqueueLocked(usageEvent{id: id, key: key, at: at, summaryOnly: true})
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
	if u.closed {
		u.mu.Unlock()
		return
	}
	done := make(chan struct{})
	u.events <- usageEvent{barrier: done}
	u.mu.Unlock()
	<-done
}

func (u *usageStore) enqueueLocked(e usageEvent) {
	if u.closed {
		return
	}
	select {
	case u.events <- e:
	default:
		u.log.Warn("usage_dropped", "resource_key", e.id)
	}
}

func (u *usageStore) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	if !u.closed {
		u.closed = true
		close(u.events)
	}
	u.mu.Unlock()
	<-u.done
}

func (u *usageStore) run() {
	defer close(u.done)
	defer u.db.Close()
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
				u.log.Warn("usage_cleanup_failed", "error", err)
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
			u.log.Error("usage_write_failed", "count", len(batch), "error", err)
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
		if !e.summaryOnly {
			if err := recordPlaybackTransfer(ctx, tx, e); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO request_events
(resource_key, requested_at, version_key, host, source, method, range_header, cache_result, outcome,
 file_bytes, transferred_bytes, elapsed_ms, status, counts_as_demand)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.id, e.at, e.key, e.host, e.source, e.method, e.rangeHeader, e.cache, e.outcome,
				e.size, e.bytes, e.elapsedMS, e.status, e.demand)
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
		if e.id == "" {
			continue
		}
		id, parseErr := strconv.ParseInt(e.id, 10, 64)
		if parseErr != nil || id <= 0 {
			return fmt.Errorf("invalid demand song ID: %q", e.id)
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
		if err == nil && e.at-last < demandWindow.Milliseconds() {
			continue
		}
		if err == sql.ErrNoRows {
			score = 1
		} else {
			score = retentionScore(score, last, e.at) + 1
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
	result, err := u.db.ExecContext(ctx, `DELETE FROM request_events WHERE event_id IN (
 SELECT event_id FROM request_events WHERE requested_at < ? ORDER BY requested_at LIMIT ?
)`, now.Add(-u.retention).UnixMilli(), requestCleanupBatch)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
