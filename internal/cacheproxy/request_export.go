package cacheproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

// ExportRequestHistory streams a private SQLite snapshot, including HEAD and
// unfinished requests, without holding the business DB open during output.
func ExportRequestHistory(ctx context.Context, path string, from, to int64, w io.Writer) (int64, error) {
	history := NewRequestHistory(path)
	defer history.Close()
	return history.Export(ctx, from, to, w)
}

func (h *RequestHistory) Export(ctx context.Context, from, to int64, w io.Writer) (int64, error) {
	if err := (RequestQuery{From: from, To: to, Limit: 1}).Validate(); err != nil {
		return 0, err
	}
	copyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	snapshot, release, err := h.snapshot(copyCtx)
	cancel()
	if err != nil {
		return 0, err
	}
	defer release()
	return exportRequestHistory(ctx, snapshot.db, from, to, w)
}

func exportRequestHistory(ctx context.Context, db *sql.DB, from, to int64, w io.Writer) (int64, error) {
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
