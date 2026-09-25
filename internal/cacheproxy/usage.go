package cacheproxy

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Counts describe observed GET demand, not confirmed playback. Deduplication is
// per song across hosts/versions, anchored at the previous counted demand.
const demandWindow = 30 * time.Second

type usageEvent struct {
	id                                                     string
	at                                                     int64
	key, host, source, method, rangeHeader, cache, outcome string
	size, bytes, elapsedMS                                 int64
	status                                                 int
	demand                                                 bool
	summaryOnly                                            bool
}

type usageStore struct {
	db     *sql.DB
	log    *slog.Logger
	mu     sync.Mutex
	closed bool
	events chan usageEvent
	done   chan struct{}
	now    func() time.Time
}

func openUsage(path string, log *slog.Logger) (*usageStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=1000;
CREATE TABLE IF NOT EXISTS song_usage (
 song_id TEXT PRIMARY KEY,
 get_count INTEGER NOT NULL,
 demand_count INTEGER NOT NULL,
 first_requested_at INTEGER NOT NULL,
 last_requested_at INTEGER NOT NULL,
 last_demand_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS request_events (
 event_id INTEGER PRIMARY KEY,
 song_id TEXT NOT NULL,
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
CREATE INDEX IF NOT EXISTS request_events_song_time ON request_events(song_id, requested_at);
CREATE INDEX IF NOT EXISTS request_events_time ON request_events(requested_at);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	u := &usageStore{db: db, log: log, events: make(chan usageEvent, 1024), done: make(chan struct{}), now: time.Now}
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

func (u *usageStore) enqueueLocked(e usageEvent) {
	if u.closed {
		return
	}
	select {
	case u.events <- e:
	default:
		u.log.Warn("usage_dropped", "song_id", e.id)
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
	for event := range u.events {
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
		if !e.summaryOnly {
			_, err = tx.ExecContext(ctx, `INSERT INTO request_events
(song_id, requested_at, version_key, host, source, method, range_header, cache_result, outcome,
 file_bytes, transferred_bytes, elapsed_ms, status, counts_as_demand)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.id, e.at, e.key, e.host, e.source, e.method, e.rangeHeader, e.cache, e.outcome,
				e.size, e.bytes, e.elapsedMS, e.status, e.demand)
			if err != nil {
				return err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO song_usage
(song_id, get_count, demand_count, first_requested_at, last_requested_at, last_demand_at)
VALUES (?, 1, 1, ?, ?, ?)
ON CONFLICT(song_id) DO UPDATE SET
 get_count = get_count + 1,
 demand_count = demand_count + CASE WHEN excluded.last_requested_at - last_demand_at >= ? THEN 1 ELSE 0 END,
 first_requested_at = min(first_requested_at, excluded.first_requested_at),
 last_requested_at = max(last_requested_at, excluded.last_requested_at),
 last_demand_at = CASE WHEN excluded.last_requested_at - last_demand_at >= ? THEN excluded.last_requested_at ELSE last_demand_at END`,
			e.id, e.at, e.at, e.at, demandWindow.Milliseconds(), demandWindow.Milliseconds())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
