package cacheproxy

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

const MaxRecentRequests = 500

type RecentRequest struct {
	Sources           *ResponseSources `json:"sources"`
	SongID            string           `json:"songID"`
	CompletedAt       *int64           `json:"completedAt"`
	ResponseBytes     *int64           `json:"responseBytes"`
	FirstBodyNS       *int64           `json:"firstBodyNS"`
	FirstBodyBlocking *bool            `json:"firstBodyBlocking"`
	ID                int64            `json:"id"`
	At                int64            `json:"at"`
	Resource          string           `json:"resource"`
	Method            string           `json:"method"`
	Range             string           `json:"range"`
	Cache             string           `json:"cache"`
	Outcome           string           `json:"outcome"`
	Bytes             int64            `json:"bytes"`
	ElapsedMS         int64            `json:"elapsedMS"`
	Status            int              `json:"status"`
}

type RecentRequests struct {
	NextCursor string          `json:"nextCursor"`
	StorageID  string          `json:"storageID"`
	Requests   []RecentRequest `json:"requests"`
	HasMore    bool            `json:"hasMore"`
}

var safeRequestRange = regexp.MustCompile(`^bytes=[0-9 ,\-]{1,200}$`)

// ReadRecentRequests reads a bounded, expanding recent window through the time
// index. It never starts the engine, creates a database, or reads signed URLs.
// The history contains HTTP video requests only; preparation uses diagnostics.
func ReadRecentRequests(ctx context.Context, databasePath string, limit int) (RecentRequests, error) {
	return readRequestPage(ctx, databasePath, RequestQuery{Limit: limit}, false)
}
func ReadRequestHistory(ctx context.Context, databasePath string, query RequestQuery) (RecentRequests, error) {
	return readRequestPage(ctx, databasePath, query, true)
}
func readRequestPage(ctx context.Context, databasePath string, query RequestQuery, getOnly bool) (RecentRequests, error) {
	limit := query.Limit
	result := RecentRequests{Requests: []RecentRequest{}}
	if err := query.Validate(); err != nil {
		return result, err
	}
	result.StorageID = DatabaseID(databasePath)
	db, err := readDatabaseFile(databasePath)
	if err != nil {
		return result, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	window, args := query.window(getOnly)
	rows, err := db.QueryContext(ctx, `SELECT event_id, requested_at, COALESCE(resource_md5,''), method, range_header,
 cache_result, outcome, transferred_bytes, elapsed_ms, status, COALESCE(CAST(song_id AS TEXT),''), completed_at, response_bytes, first_body_ns, first_body_blocking, download_bytes, cache_bytes, local_bytes
 FROM (`+window+`) e
 ORDER BY requested_at DESC, event_id DESC`, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var r RecentRequest
		var download, cache, local *int64
		if err = rows.Scan(&r.ID, &r.At, &r.Resource, &r.Method, &r.Range, &r.Cache, &r.Outcome, &r.Bytes, &r.ElapsedMS, &r.Status, &r.SongID, &r.CompletedAt, &r.ResponseBytes, &r.FirstBodyNS, &r.FirstBodyBlocking, &download, &cache, &local); err != nil {
			return result, err
		}
		if download != nil && cache != nil && local != nil {
			r.Sources = &ResponseSources{DownloadBytes: *download, CacheBytes: *cache, LocalBytes: *local}
		}
		count++
		if count > limit {
			result.HasMore = true
			break
		}
		if r.Range != "" && !safeRequestRange.MatchString(r.Range) {
			r.Range = "非标准 Range"
		}
		result.Requests = append(result.Requests, r)
	}
	if result.HasMore {
		last := result.Requests[len(result.Requests)-1]
		result.NextCursor = fmt.Sprintf("%d:%d", last.At, last.ID)
	}
	return result, rows.Err()
}
