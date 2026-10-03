package cacheproxy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Counts describe observed GET demand, not confirmed playback. Deduplication is
// per resource across hosts, anchored at the previous counted demand.
const demandWindow = 30 * time.Second

const requestCleanupBatch = 500

type usageEvent struct {
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=1000;
CREATE TABLE IF NOT EXISTS songs (
 song_id TEXT PRIMARY KEY,
 title TEXT NOT NULL DEFAULT '',
 metadata_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS video_versions (
 version_key TEXT PRIMARY KEY,
 checksum TEXT NOT NULL,
 file_bytes INTEGER NOT NULL,
 source_path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS song_videos (
 song_id TEXT NOT NULL REFERENCES songs(song_id),
 version_key TEXT NOT NULL REFERENCES video_versions(version_key),
 PRIMARY KEY(song_id, version_key)
);
CREATE INDEX IF NOT EXISTS song_videos_version ON song_videos(version_key);
CREATE TABLE IF NOT EXISTS current_videos (
 song_id TEXT PRIMARY KEY,
 version_key TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS current_videos_version ON current_videos(version_key);
CREATE TABLE IF NOT EXISTS resource_usage (
 resource_key TEXT PRIMARY KEY,
 get_count INTEGER NOT NULL,
 demand_count INTEGER NOT NULL,
 first_requested_at INTEGER NOT NULL,
 last_requested_at INTEGER NOT NULL,
 last_demand_at INTEGER NOT NULL,
 demand_score REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS request_events (
 event_id INTEGER PRIMARY KEY,
 resource_key TEXT NOT NULL,
 requested_at INTEGER NOT NULL,
 version_key TEXT NOT NULL,
 host TEXT NOT NULL,
 source TEXT NOT NULL,
 method TEXT NOT NULL,
 range_header TEXT NOT NULL,
 cache_result TEXT NOT NULL,
 outcome TEXT NOT NULL,
 file_bytes INTEGER NOT NULL,
 transferred_bytes INTEGER NOT NULL,
 elapsed_ms INTEGER NOT NULL,
 status INTEGER NOT NULL,
 counts_as_demand INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS request_events_resource_time ON request_events(resource_key, requested_at);
CREATE INDEX IF NOT EXISTS request_events_time ON request_events(requested_at);`)
	if err == nil {
		_, err = db.Exec(recentHTTPIndexSQL)
	}
	// This schema requires a fresh store; there is no legacy score migration.
	if err == nil {
		var rows *sql.Rows
		rows, err = db.Query(`SELECT demand_score FROM resource_usage LIMIT 0`)
		if err == nil {
			rows.Close()
		} else {
			err = fmt.Errorf("storage requires the exponential demand schema; use a new storage directory: %w", err)
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	u := &usageStore{retention: time.Duration(retentionDays) * 24 * time.Hour, db: db, log: log, events: make(chan usageEvent, 1024), done: make(chan struct{}), now: time.Now}
	if err := initializeTraffic(db); err != nil {
		db.Close()
		return nil, err
	}
	go u.run()
	return u, nil
}

// Timestamp and enqueue under the same lock: response completion cannot reorder
// demand observations. The database worker remains off the HTTP request path.
func (u *usageStore) startDemand(id string) int64 {
	if u == nil {
		return time.Now().UnixMilli()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	at := u.now().UnixMilli()
	u.enqueueLocked(usageEvent{id: id, at: at, demand: true, summaryOnly: true})
	return at
}

func (u *usageStore) record(e usageEvent) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
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
		var score float64
		var last int64
		err = tx.QueryRowContext(ctx, `SELECT demand_score, last_demand_at FROM resource_usage WHERE resource_key = ?`, e.id).Scan(&score, &last)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			score = 1
		} else if e.at-last >= demandWindow.Milliseconds() {
			score = retentionScore(score, last, e.at) + 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_usage
(resource_key, get_count, demand_count, first_requested_at, last_requested_at, last_demand_at, demand_score)
VALUES (?, 1, 1, ?, ?, ?, ?)
ON CONFLICT(resource_key) DO UPDATE SET
 get_count = get_count + 1,
 demand_score = excluded.demand_score,
 demand_count = demand_count + CASE WHEN excluded.last_requested_at - last_demand_at >= ? THEN 1 ELSE 0 END,
 first_requested_at = min(first_requested_at, excluded.first_requested_at),
 last_requested_at = max(last_requested_at, excluded.last_requested_at),
 last_demand_at = CASE WHEN excluded.last_requested_at - last_demand_at >= ? THEN excluded.last_requested_at ELSE last_demand_at END`,
			e.id, e.at, e.at, e.at, score, demandWindow.Milliseconds(), demandWindow.Milliseconds())
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
