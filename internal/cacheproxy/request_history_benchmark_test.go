package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Includes setup outside the timed loop; use -benchtime=1x for a paired large
// retained-history check without writing through the request collector.
func BenchmarkRequestHistoryRetained(b *testing.B) {
	for _, size := range []int{10000, 1000000, 3000000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			root := b.TempDir()
			u, err := fixtureUsage(b, storagePath(root), slog.New(slog.NewTextHandler(io.Discard, nil)), 0, true)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { u.close() })
			if _, err := u.db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
INSERT INTO request_events(event_id,requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,resource_md5)
SELECT i,i*1000,'GET','',CASE WHEN i%2=1 THEN 'canceled' ELSE 'completed' END,1,1,206,? FROM n`, size, strings.Repeat("a", 32)); err != nil {
				b.Fatal(err)
			}
			query, args := (RequestQuery{Limit: 50, HideAdjacentCanceled: true}).window(true)
			rows, err := u.db.Query("EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					b.Fatal(err)
				}
				b.Log(detail)
			}
			if err := rows.Err(); err != nil {
				b.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				b.Fatal(err)
			}
			b.Run("page", func(b *testing.B) {
				for b.Loop() {
					page, err := ReadRequestHistory(b.Context(), storagePath(root), RequestQuery{Limit: 50, HideAdjacentCanceled: true})
					if err != nil || len(page.Requests) != 50 || !page.HasMore {
						b.Fatalf("bounded filtered page: %d rows, %v", len(page.Requests), err)
					}
				}
			})
			b.Run("next-page", func(b *testing.B) {
				q := RequestQuery{Limit: 50, HideAdjacentCanceled: true}
				first, err := ReadRequestHistory(b.Context(), storagePath(root), q)
				if err != nil || first.NextCursor == "" {
					b.Fatalf("first page: %+v %v", first, err)
				}
				q.Cursor = first.NextCursor
				for b.Loop() {
					page, err := ReadRequestHistory(b.Context(), storagePath(root), q)
					if err != nil || len(page.Requests) != 50 || page.Requests[0].ID >= first.Requests[len(first.Requests)-1].ID {
						b.Fatalf("next filtered page: %+v %v", page, err)
					}
				}
			})
		})
	}
}

func BenchmarkRequestHistoryQueries(b *testing.B) {
	root := b.TempDir()
	u, err := fixtureUsage(b, filepath.Join(root, storageDatabaseName), slog.New(slog.NewTextHandler(io.Discard, nil)), 0, true)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { u.close() })
	now := time.Now()
	ready := false
	for i := 0; i < 10000; i += 100 {
		events := make([]usageEvent, 100)
		for j := range events {
			outcome := "completed"
			if (i+j)%2 == 1 {
				outcome = "canceled"
			}
			events[j] = usageEvent{at: now.Add(-time.Duration(i+j) * time.Second).UnixMilli(), key: strings.Repeat("a", 32), outcome: outcome, method: "GET", status: 200, bytes: 1, firstBodyNS: int64(i+j+1) * int64(time.Millisecond), firstBodyBlocking: &ready}
		}
		if err := u.write(events); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("page-50-of-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ReadRequestHistory(context.Background(), storagePath(root), RequestQuery{Limit: 50, From: now.Add(-72 * time.Hour).UnixMilli()}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("filtered-page-50-of-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			page, err := ReadRequestHistory(context.Background(), storagePath(root), RequestQuery{Limit: 50, From: now.Add(-72 * time.Hour).UnixMilli(), HideAdjacentCanceled: true})
			if err != nil || len(page.Requests) != 50 {
				b.Fatalf("filtered page: %d rows, %v", len(page.Requests), err)
			}
		}
	})
	b.Run("recent-statistics-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ReadRequestStatistics(context.Background(), storagePath(root), now); err != nil {
				b.Fatal(err)
			}
		}
	})
}
