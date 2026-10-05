package cacheproxy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxRecentRequests = 500

const recentHTTPIndexSQL = `CREATE INDEX IF NOT EXISTS request_events_http_time
 ON request_events(requested_at DESC, event_id DESC) WHERE source='http'`

const recentHTTPWindowSQL = `SELECT * FROM request_events INDEXED BY request_events_http_time
 WHERE source='http' ORDER BY requested_at DESC, event_id DESC LIMIT ?`

type RecentSong struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type RecentRequest struct {
	ID        int64        `json:"id"`
	At        int64        `json:"at"`
	Resource  string       `json:"resource"`
	Method    string       `json:"method"`
	Range     string       `json:"range"`
	Cache     string       `json:"cache"`
	Outcome   string       `json:"outcome"`
	Bytes     int64        `json:"bytes"`
	ElapsedMS int64        `json:"elapsedMS"`
	Status    int          `json:"status"`
	Songs     []RecentSong `json:"songs"`
	MoreSongs bool         `json:"moreSongs"`
}

type RecentRequests struct {
	StorageID string          `json:"storageID"`
	Requests  []RecentRequest `json:"requests"`
	HasMore   bool            `json:"hasMore"`
}

var safeRequestRange = regexp.MustCompile(`^bytes=[0-9 ,\-]{1,200}$`)

// ReadRecentRequests reads a bounded, expanding recent window through the time
// index. It never starts the engine, creates a database, or reads signed URLs.
// Existing databases receive the HTTP partial index once, even with CDN stopped.
// Only HTTP video requests are included; prefetch events are not client bytes.
func ReadRecentRequests(ctx context.Context, root string, limit int) (RecentRequests, error) {
	result := RecentRequests{Requests: []RecentRequest{}}
	if limit < 1 || limit > MaxRecentRequests {
		return result, errors.New("invalid recent request limit")
	}
	path, err := filepath.Abs(filepath.Join(root, "stepstash.sqlite"))
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256([]byte(path))
	result.StorageID = hex.EncodeToString(hash[:])
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	uriPath := filepath.ToSlash(path)
	if len(uriPath) > 1 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err = checkStorageFormat(root); err != nil {
		return result, err
	}
	if err = ensureRecentHTTPIndex(ctx, db, u); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT event_id, requested_at, resource_key, method, range_header,
 cache_result, outcome, transferred_bytes, elapsed_ms, status, file_bytes,
 (SELECT json_group_array(json_object('id',song_id,'title',title)) FROM
   (SELECT CAST(s.song_id AS TEXT) song_id, COALESCE(substr(s.name,1,300),'') AS title FROM song_media sv
    JOIN songs s ON s.song_id=sv.song_id WHERE sv.md5=e.version_key
    ORDER BY sv.rowid LIMIT 21))
 FROM (`+recentHTTPWindowSQL+`) e
 ORDER BY requested_at DESC, event_id DESC`, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var r RecentRequest
		var songs string
		var size int64
		if err = rows.Scan(&r.ID, &r.At, &r.Resource, &r.Method, &r.Range, &r.Cache, &r.Outcome, &r.Bytes, &r.ElapsedMS, &r.Status, &size, &songs); err != nil {
			return result, err
		}
		count++
		if count > limit {
			result.HasMore = true
			break
		}
		if err = json.Unmarshal([]byte(songs), &r.Songs); err != nil {
			return result, err
		}
		if len(r.Songs) > 20 {
			r.MoreSongs = true
			r.Songs = r.Songs[:20]
		}
		// Older completed records can still describe a short body. Do not change
		// stored outcomes or cumulative traffic statistics when interpreting them.
		if r.Outcome == "completed" && r.Method == "GET" {
			if expected := expectedRecentBytes(r.Status, r.Range, size); expected >= 0 && r.Bytes < expected {
				r.Outcome = "incomplete"
			}
		}
		if r.Range != "" && !safeRequestRange.MatchString(r.Range) {
			r.Range = "非标准 Range"
		}
		result.Requests = append(result.Requests, r)
	}
	return result, rows.Err()
}

// The normal reader stays read-only. Only a missing index opens a separate
// mode=rw connection; mode=rw cannot create a missing database. Once migrated,
// polling never rebuilds an index or scans prefetch events. INDEXED BY makes a
// missing index an error rather than silently falling back to a table scan.
func ensureRecentHTTPIndex(ctx context.Context, db *sql.DB, uri url.URL) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='request_events_http_time'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	q := uri.Query()
	q.Set("mode", "rw")
	uri.RawQuery = q.Encode()
	writer, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.ExecContext(ctx, recentHTTPIndexSQL)
	return err
}

func expectedRecentBytes(status int, header string, size int64) int64 {
	if size <= 0 {
		return -1
	}
	if status == 200 {
		return size
	}
	if status != 206 || !strings.HasPrefix(header, "bytes=") {
		return -1
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 {
		return -1
	}
	if parts[0] == "" {
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 {
			return -1
		}
		return min(n, size)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return -1
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return -1
		}
	}
	return min(end, size-1) - start + 1
}
