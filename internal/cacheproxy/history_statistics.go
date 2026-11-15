package cacheproxy

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

const historyStatisticsLimit = 10000
const historyStatisticsTTL = time.Minute
const historyStatisticsWindow = 72 * time.Hour

type LatencyGroup struct {
	Samples int64    `json:"samples"`
	P50MS   *float64 `json:"p50MS"`
	P95MS   *float64 `json:"p95MS"`
}

// From is inclusive, To exclusive and also the calculation time. Requests is
// the raw window size, before either metric's validity filter is applied.
type RequestStatistics struct {
	From          int64           `json:"from"`
	To            int64           `json:"to"`
	OldestAt      *int64          `json:"oldestAt"`
	Limit         int             `json:"limit"`
	Requests      int             `json:"requests"`
	SourceSamples int64           `json:"sourceSamples"`
	Sources       ResponseSources `json:"sources"`
	Blocking      LatencyGroup    `json:"blocking"`
	Ready         LatencyGroup    `json:"ready"`
}

func ReadRequestStatistics(ctx context.Context, path string, now time.Time) (RequestStatistics, error) {
	h := NewRequestHistory(path)
	defer h.Close()
	return h.Statistics(ctx, now)
}

func (h *RequestHistory) Statistics(ctx context.Context, now time.Time) (RequestStatistics, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel)
	defer stop()
	if err := h.ctx.Err(); err != nil {
		return RequestStatistics{}, err
	}
	value, err := h.statistics.get(ctx, func(v RequestStatistics) bool {
		age := now.UnixMilli() - v.To
		return age >= 0 && age < historyStatisticsTTL.Milliseconds()
	}, func() (RequestStatistics, error) {
		return readRequestStatistics(ctx, h.path, now)
	})
	// Callers cannot mutate the cached quantiles or coverage boundary.
	if value.OldestAt != nil {
		n := *value.OldestAt
		value.OldestAt = &n
	}
	for _, g := range []*LatencyGroup{&value.Ready, &value.Blocking} {
		if g.P50MS != nil {
			n := *g.P50MS
			g.P50MS = &n
		}
		if g.P95MS != nil {
			n := *g.P95MS
			g.P95MS = &n
		}
	}
	return value, err
}

const historyStatisticsSQL = `SELECT requested_at,method,status,transferred_bytes,
 first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes
 FROM request_events INDEXED BY request_events_time
 WHERE requested_at>=? AND requested_at<?
 ORDER BY requested_at DESC,event_id DESC LIMIT ?`

type statisticsRequest struct {
	at, bytes              int64
	method                 string
	status                 int
	firstBody, blocking    sql.NullInt64
	download, cache, local sql.NullInt64
}

func readStatisticsRequests(ctx context.Context, path string, from, to int64) ([]statisticsRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	db, err := readDatabaseFile(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, historyStatisticsSQL, from, to, historyStatisticsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]statisticsRequest, 0, historyStatisticsLimit)
	for rows.Next() {
		var r statisticsRequest
		if err := rows.Scan(&r.at, &r.method, &r.status, &r.bytes, &r.firstBody, &r.blocking, &r.download, &r.cache, &r.local); err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

func readRequestStatistics(ctx context.Context, path string, now time.Time) (RequestStatistics, error) {
	v := RequestStatistics{From: now.Add(-historyStatisticsWindow).UnixMilli(), To: now.UnixMilli(), Limit: historyStatisticsLimit}
	requests, err := readStatisticsRequests(ctx, path, v.From, v.To)
	if err != nil {
		return v, err
	}
	// All SQLite rows/connections have been released before aggregation/sorting.
	v.Requests = len(requests)
	if len(requests) > 0 {
		at := requests[len(requests)-1].at
		v.OldestAt = &at
	}
	var ready, blocking []int64
	for _, r := range requests {
		if r.method != "GET" {
			continue
		}
		if r.download.Valid && r.cache.Valid && r.local.Valid {
			v.SourceSamples++
			v.Sources.DownloadBytes += r.download.Int64
			v.Sources.CacheBytes += r.cache.Int64
			v.Sources.LocalBytes += r.local.Int64
		}
		if (r.status != 200 && r.status != 206) || r.bytes <= 0 || !r.firstBody.Valid || !r.blocking.Valid {
			continue
		}
		if r.blocking.Int64 == 1 {
			blocking = append(blocking, r.firstBody.Int64)
		} else {
			ready = append(ready, r.firstBody.Int64)
		}
	}
	v.Ready, v.Blocking = latencyQuantiles(ready), latencyQuantiles(blocking)
	return v, ctx.Err()
}

func latencyQuantiles(ns []int64) LatencyGroup {
	g := LatencyGroup{Samples: int64(len(ns))}
	if n := len(ns); n > 0 {
		slices.Sort(ns)
		p50 := float64(ns[(n+1)/2-1]) / float64(time.Millisecond)
		p95 := float64(ns[(95*n+99)/100-1]) / float64(time.Millisecond)
		g.P50MS, g.P95MS = &p50, &p95
	}
	return g
}
