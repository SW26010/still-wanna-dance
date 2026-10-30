package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	b.Run("quantiles-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ReadFirstBodyLatency(context.Background(), storagePath(root), now); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("source-totals-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ReadResponseSources(context.Background(), storagePath(root)); err != nil {
				b.Fatal(err)
			}
		}
	})
}
