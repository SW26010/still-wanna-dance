package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Read recency in bounded batches, independently of request detail retention.
func loadLibraryRecency(ctx context.Context, db *sql.DB, resources map[string]*libraryCandidate) error {
	const batchSize = 256
	keys := make([]string, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); start += batchSize {
		encoded, err := json.Marshal(keys[start:min(start+batchSize, len(keys))])
		if err != nil {
			return err
		}
		if err := readLibraryRecency(ctx, db, string(encoded), resources); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func readLibraryRecency(ctx context.Context, db *sql.DB, keys string, batch map[string]*libraryCandidate) error {
	rows, err := db.QueryContext(ctx, `SELECT v.md5,
 max(COALESCE((SELECT last_requested_at FROM media_access WHERE md5=v.md5),0),
 COALESCE((SELECT max(requested_at) FROM request_events WHERE resource_md5=v.md5),0))
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
