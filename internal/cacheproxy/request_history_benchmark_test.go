package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkRequestHistoryQueries(b *testing.B) {
	root := b.TempDir()
	u, err := fixtureUsage(b, filepath.Join(root, storageDatabaseName), slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { u.close() })
	now := time.Now()
	ready := false
	for i := 0; i < 10000; i += 100 {
		events := make([]usageEvent, 100)
		for j := range events {
			events[j] = usageEvent{at: now.Add(-time.Duration(i+j) * time.Second).UnixMilli(), source: "http", method: "GET", status: 200, bytes: 1, firstBodyNS: int64(i+j+1) * int64(time.Millisecond), firstBodyBlocking: &ready}
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
	b.Run("quantiles-10000", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ReadFirstBodyLatency(context.Background(), storagePath(root), now); err != nil {
				b.Fatal(err)
			}
		}
	})
}
