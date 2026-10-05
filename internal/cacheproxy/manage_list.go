package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// Candidates contain only what filtering and sorting need. File identities and
// display details are deliberately left to readCacheEntry after pagination.
type cacheListCandidate struct {
	key         string
	bytes       int64
	lastRequest int64
	title       string
	match       bool
}

func loadCacheListMetadata(ctx context.Context, db *sql.DB, candidates []cacheListCandidate, query, order string) error {
	if db == nil || (order != "recent" && order != "title" && query == "") {
		return ctx.Err()
	}
	// Bound each SQL result to a directory batch, including when the catalog
	// contains many versions whose files are no longer cached.
	const batchSize = 256
	for start := 0; start < len(candidates); start += batchSize {
		end := min(start+batchSize, len(candidates))
		batch := make(map[string]*cacheListCandidate, end-start)
		keys := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			c := &candidates[i]
			batch[c.key] = c
			keys = append(keys, c.key)
		}
		encoded, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		if order == "recent" {
			if err := loadCacheListRecency(ctx, db, string(encoded), batch); err != nil {
				return err
			}
		}
		if order == "title" || query != "" {
			if err := loadCacheListSongs(ctx, db, string(encoded), query, batch); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func loadCacheListRecency(ctx context.Context, db *sql.DB, keys string, batch map[string]*cacheListCandidate) error {
	rows, err := db.QueryContext(ctx, `SELECT v.md5,
 max(COALESCE((SELECT last_requested_at FROM media_access WHERE md5=v.md5),0),
 COALESCE((SELECT max(requested_at) FROM request_events WHERE resource_key=v.md5 AND source='http'),0))
 FROM media v
 WHERE v.md5 IN (SELECT value FROM json_each(?))`, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var last int64
		if err := rows.Scan(&key, &last); err != nil {
			return err
		}
		batch[key].lastRequest = last
	}
	return rows.Err()
}

func loadCacheListSongs(ctx context.Context, db *sql.DB, keys, query string, batch map[string]*cacheListCandidate) error {
	rows, err := db.QueryContext(ctx, `SELECT sv.md5, s.song_id, COALESCE(substr(s.name,1,300),''),
 (?<>'' AND (instr(lower(COALESCE(s.name,'')),?)>0 OR instr(s.song_id,?)>0)), v.md5 IS NOT NULL
 FROM song_media sv JOIN songs s ON s.song_id=sv.song_id
 LEFT JOIN media v ON v.md5=sv.md5
 WHERE sv.md5 IN (SELECT value FROM json_each(?))
 ORDER BY sv.md5, s.song_id`, query, query, query, keys)
	if err != nil {
		return err
	}
	defer rows.Close()
	previous, count := "", 0
	for rows.Next() {
		var key, id, title string
		var sqlMatch, known bool
		if err := rows.Scan(&key, &id, &title, &sqlMatch, &known); err != nil {
			return err
		}
		if key != previous {
			previous, count = key, 0
		}
		c := batch[key]
		if count == 0 && known {
			c.title = title
		}
		// Preserve both existing search paths: Go's Unicode case folding for
		// the first 20 display titles, SQLite matching over all full titles.
		c.match = c.match || sqlMatch || (known && count < 20 &&
			(strings.Contains(strings.ToLower(title), query) || strings.Contains(id, query)))
		count++
	}
	return rows.Err()
}

func sortCacheList(candidates []cacheListCandidate, order string) []cacheListCandidate {
	matched := candidates[:0]
	for _, c := range candidates {
		if c.match {
			matched = append(matched, c)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if order == "size" && a.bytes != b.bytes {
			return a.bytes > b.bytes
		}
		if order == "recent" && a.lastRequest != b.lastRequest {
			return a.lastRequest > b.lastRequest
		}
		if order == "title" && a.title != b.title {
			return a.title < b.title
		}
		return a.key < b.key
	})
	return matched
}
