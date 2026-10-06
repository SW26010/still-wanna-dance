package cacheproxy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uriPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath = filepath.ToSlash(uriPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var format, existing int
	if err := db.QueryRow("PRAGMA user_version").Scan(&format); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&existing); err != nil {
		db.Close()
		return nil, err
	}
	if (existing > 0 && format != 3) || (format != 0 && format != 3) {
		db.Close()
		return nil, fmt.Errorf("旧存储格式不兼容歌曲目录格式，请选择新的存储目录；旧数据未修改")
	}
	_, err = db.Exec(`PRAGMA busy_timeout=1000; PRAGMA foreign_keys=ON; BEGIN; PRAGMA user_version=3;
CREATE TABLE IF NOT EXISTS songs (
 song_id INTEGER PRIMARY KEY CHECK(song_id>0),
 name TEXT, artist TEXT, dancer TEXT, player_count INTEGER, volume REAL,
 start REAL, end REAL,
 flip INTEGER CHECK(flip IN (0,1)), double_width INTEGER CHECK(double_width IN (0,1)),
 skip_random INTEGER CHECK(skip_random IN (0,1)), disable_public INTEGER CHECK(disable_public IN (0,1)),
 rpe INTEGER, genre TEXT, group_name TEXT, composed_title TEXT, composed_title_spell TEXT, aya_id TEXT,
 tags_json TEXT CHECK(json_valid(tags_json) AND json_type(tags_json)='array'),
 original_urls_json TEXT CHECK(json_valid(original_urls_json) AND json_type(original_urls_json)='array'),
 shader_motion_json TEXT CHECK(json_valid(shader_motion_json) AND json_type(shader_motion_json)='array')
);
CREATE TABLE IF NOT EXISTS media (
 md5 TEXT PRIMARY KEY NOT NULL CHECK(length(md5)=32 AND md5 NOT GLOB '*[^0-9a-f]*'),
 byte_size INTEGER CHECK(byte_size>=0), source_path TEXT
);
CREATE TABLE IF NOT EXISTS song_media (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 md5 TEXT NOT NULL REFERENCES media(md5)
);
CREATE INDEX IF NOT EXISTS song_media_md5 ON song_media(md5);
CREATE TABLE IF NOT EXISTS song_urls (
 song_id INTEGER NOT NULL CHECK(song_id>0),
 api TEXT NOT NULL, node TEXT NOT NULL, url TEXT NOT NULL,
 md5 TEXT NOT NULL CHECK(length(md5)=32), byte_size INTEGER NOT NULL CHECK(byte_size>0),
 query_started_at INTEGER NOT NULL, observed_at INTEGER NOT NULL,
 failed_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(song_id, api, node)
);
CREATE TABLE IF NOT EXISTS song_usage (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 demand_count INTEGER NOT NULL CHECK(demand_count>=0),
 demand_score REAL NOT NULL CHECK(demand_score>=0),
 last_demand_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS song_playback (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id),
 transfer_count INTEGER NOT NULL, last_transfer_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS playback_latency (
 cache_result TEXT PRIMARY KEY, samples INTEGER NOT NULL, first_body_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_state (
 catalog_key TEXT PRIMARY KEY, revision TEXT NOT NULL, digest TEXT NOT NULL,
 source TEXT NOT NULL, accepted_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_checks (
 catalog_key TEXT PRIMARY KEY, checked_at INTEGER NOT NULL DEFAULT 0,
 message TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS catalog_members (
 song_id INTEGER PRIMARY KEY REFERENCES songs(song_id)
);
CREATE TABLE IF NOT EXISTS media_access (
 md5 TEXT PRIMARY KEY NOT NULL,
 last_requested_at INTEGER NOT NULL
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
CREATE INDEX IF NOT EXISTS request_events_time ON request_events(requested_at); COMMIT;`)
	if err == nil {
		_, err = db.Exec(recentHTTPIndexSQL)
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
