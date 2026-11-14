package cacheproxy

import (
	"context"
	"encoding/json"
	"io"
)

// ExportRequestHistory streams one SQLite read snapshot, including HEAD and
// unfinished requests. It preserves stored fields, without current catalog joins.
func ExportRequestHistory(ctx context.Context, path string, from, to int64, w io.Writer) (int64, error) {
	if err := (RequestQuery{From: from, To: to, Limit: 1}).Validate(); err != nil {
		return 0, err
	}
	db, err := readDatabaseFile(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT * FROM request_events WHERE requested_at>=? AND requested_at<? ORDER BY requested_at,event_id`, from, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	encoder := json.NewEncoder(w)
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return count, err
		}
		record := make(map[string]any, len(columns))
		for i, name := range columns {
			if b, ok := values[i].([]byte); ok {
				values[i] = string(b)
			}
			record[name] = values[i]
		}
		if err := encoder.Encode(record); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}
