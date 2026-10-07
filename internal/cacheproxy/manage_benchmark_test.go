package cacheproxy

import (
	"context"
	"os"
	"testing"
	"time"
)

// BenchmarkReadCachePageExisting opts into read-only measurements of a real
// cache. Point STEPSTASH_BENCH_CACHE_ROOT at the directory containing videos
// and storage.sqlite. It never starts the engine or reads video contents.
func BenchmarkReadCachePageExisting(b *testing.B) {
	root := os.Getenv("STEPSTASH_BENCH_CACHE_ROOT")
	if root == "" {
		b.Skip("set STEPSTASH_BENCH_CACHE_ROOT to benchmark an existing cache")
	}
	for _, tc := range []struct {
		name, query, order string
		offset             int
	}{
		{"size_first", "", "size", 0},
		{"size_second", "", "size", 50},
		{"size_deep", "", "size", 9950},
		{"recent_first", "", "recent", 0},
		{"title_first", "", "title", 0},
		{"search_missing", "stepstash-benchmark-no-match-7c91", "title", 0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				page, err := ReadCachePage(ctx, root, tc.query, tc.order, tc.offset)
				cancel()
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(page.Total), "matches")
				if len(page.Entries) > 50 {
					b.Fatalf("page exceeds limit: %d", len(page.Entries))
				}
			}
		})
	}
}
