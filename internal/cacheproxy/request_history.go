package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// From is inclusive and To exclusive, both in Unix milliseconds. The cursor
// is a stable (arrival, row ID) boundary, independent of later completions.
type RequestQuery struct {
	From, To int64
	Cursor   string
	Limit    int
}

func (q RequestQuery) Validate() error {
	if q.Limit < 1 || q.Limit > MaxRecentRequests || q.From < 0 || q.To < 0 || (q.To != 0 && q.To <= q.From) {
		return errors.New("invalid request range or limit")
	}
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 2 {
			return errors.New("invalid request cursor")
		}
		at, e1 := strconv.ParseInt(parts[0], 10, 64)
		id, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil || at < 0 || id <= 0 {
			return errors.New("invalid request cursor")
		}
	}
	return nil
}

func (q RequestQuery) window(getOnly bool) (string, []any) {
	where := "source='http'"
	var args []any
	if getOnly {
		where += " AND method='GET'"
	}
	if q.From > 0 {
		where += " AND requested_at>=?"
		args = append(args, q.From)
	}
	if q.To > 0 {
		where += " AND requested_at<?"
		args = append(args, q.To)
	}
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		at, _ := strconv.ParseInt(parts[0], 10, 64)
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		where += " AND (requested_at,event_id)<(?,?)"
		args = append(args, at, id)
	}
	return "SELECT * FROM request_events INDEXED BY request_events_http_time WHERE " + where + " ORDER BY requested_at DESC,event_id DESC LIMIT ?", append(args, q.Limit+1)
}

type LatencyGroup struct {
	Samples int64    `json:"samples"`
	P50MS   *float64 `json:"p50MS"`
	P95MS   *float64 `json:"p95MS"`
}
type FirstBodyLatency struct {
	From          int64        `json:"from"`
	To            int64        `json:"to"`
	AvailableFrom *int64       `json:"availableFrom"`
	Partial       bool         `json:"partial"`
	Blocking      LatencyGroup `json:"blocking"`
	Ready         LatencyGroup `json:"ready"`
}

// Exact nearest-rank percentiles are calculated in SQLite; only two aggregate
// rows reach the UI. Retention and collection coverage survive process restarts.
func ReadFirstBodyLatency(ctx context.Context, databasePath string, now time.Time) (FirstBodyLatency, error) {
	r := FirstBodyLatency{From: now.Add(-72 * time.Hour).UnixMilli(), To: now.UnixMilli(), Partial: true}
	db, err := readDatabaseFile(databasePath)
	if err != nil {
		return r, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var start, retained int64
	err = db.QueryRowContext(ctx, "SELECT started_at,retained_since FROM request_observation WHERE id=1").Scan(&start, &retained)
	if err == sql.ErrNoRows {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	available := max(r.From, start, retained)
	r.AvailableFrom = &available
	r.Partial = available > r.From
	rows, err := db.QueryContext(ctx, `WITH samples AS (
 SELECT first_body_blocking AS blocking,first_body_ns AS ns,
 row_number() OVER (PARTITION BY first_body_blocking ORDER BY first_body_ns) AS rank,
 count(*) OVER (PARTITION BY first_body_blocking) AS n
 FROM request_events INDEXED BY request_events_http_time
 WHERE source='http' AND method='GET' AND requested_at>=? AND requested_at<?
 AND status IN (200,206) AND transferred_bytes>0 AND first_body_ns IS NOT NULL AND first_body_blocking IS NOT NULL
 ) SELECT blocking,max(n),max(CASE WHEN rank=(n+1)/2 THEN ns END),
 max(CASE WHEN rank=(95*n+99)/100 THEN ns END) FROM samples GROUP BY blocking`, r.From, r.To)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var blocked bool
		var group LatencyGroup
		var p50, p95 int64
		if err := rows.Scan(&blocked, &group.Samples, &p50, &p95); err != nil {
			return r, err
		}
		a, b := float64(p50)/float64(time.Millisecond), float64(p95)/float64(time.Millisecond)
		group.P50MS = &a
		group.P95MS = &b
		if blocked {
			r.Blocking = group
		} else {
			r.Ready = group
		}
	}
	return r, rows.Err()
}
