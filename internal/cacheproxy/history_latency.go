package cacheproxy

import (
	"context"
	"database/sql"
	"time"
)

const historyLatencySamples = `method='GET' AND status IN (200,206) AND transferred_bytes>0 AND first_body_ns IS NOT NULL AND first_body_blocking IS NOT NULL`
const historyLatencyIndex = `CREATE INDEX IF NOT EXISTS request_events_history_latency ON request_events(first_body_blocking,first_body_ns,requested_at) WHERE ` + historyLatencySamples

type historyLatency struct {
	value        FirstBodyLatency
	oldest, next *int64 // Earliest included sample and next future arrival.
}

func (v historyLatency) contains(now time.Time) bool {
	to := now.UnixMilli()
	return to >= v.value.To && (v.oldest == nil || now.Add(-72*time.Hour).UnixMilli() <= *v.oldest) && (v.next == nil || to <= *v.next)
}

func (v historyLatency) result(now time.Time) FirstBodyLatency {
	r := v.value
	r.From, r.To = now.Add(-72*time.Hour).UnixMilli(), now.UnixMilli()
	// Do not expose mutable pointers held by the shared cache.
	for _, g := range []*LatencyGroup{&r.Ready, &r.Blocking} {
		if g.P50MS != nil {
			n := *g.P50MS
			g.P50MS = &n
		}
		if g.P95MS != nil {
			n := *g.P95MS
			g.P95MS = &n
		}
	}
	return r
}

// The partial covering index supplies exact nearest-rank quantiles without
// materializing/window-sorting every sample. Read the upper percentile from
// the high end so only the final five percent are skipped.
func readHistoryLatency(ctx context.Context, db *sql.DB, now time.Time) (historyLatency, error) {
	v := historyLatency{value: FirstBodyLatency{From: now.Add(-72 * time.Hour).UnixMilli(), To: now.UnixMilli()}}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT first_body_blocking,
 count(CASE WHEN requested_at<? THEN 1 END),
 min(CASE WHEN requested_at<? THEN requested_at END),
 min(CASE WHEN requested_at>=? THEN requested_at END)
 FROM request_events WHERE `+historyLatencySamples+` AND requested_at>=? GROUP BY first_body_blocking`, v.value.To, v.value.To, v.value.To, v.value.From)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var blocking bool
		var n int64
		var oldest, next *int64
		if err := rows.Scan(&blocking, &n, &oldest, &next); err != nil {
			rows.Close()
			return v, err
		}
		if oldest != nil && (v.oldest == nil || *oldest < *v.oldest) {
			v.oldest = oldest
		}
		if next != nil && (v.next == nil || *next < *v.next) {
			v.next = next
		}
		if blocking {
			v.value.Blocking.Samples = n
		} else {
			v.value.Ready.Samples = n
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	for i, group := range []*LatencyGroup{&v.value.Ready, &v.value.Blocking} {
		n := group.Samples
		if n == 0 {
			continue
		}
		for _, p := range []struct {
			order  string
			offset int64
			target **float64
		}{
			{"ASC", (n+1)/2 - 1, &group.P50MS},
			{"DESC", n - (95*n+99)/100, &group.P95MS},
		} {
			var ns int64
			err := db.QueryRowContext(ctx, `SELECT first_body_ns FROM request_events WHERE `+historyLatencySamples+`
 AND first_body_blocking=? AND requested_at>=? AND requested_at<? ORDER BY first_body_ns `+p.order+` LIMIT 1 OFFSET ?`, i, v.value.From, v.value.To, p.offset).Scan(&ns)
			if err != nil {
				return v, err
			}
			ms := float64(ns) / float64(time.Millisecond)
			*p.target = &ms
		}
	}
	return v, nil
}
