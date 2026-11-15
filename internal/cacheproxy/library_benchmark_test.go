package cacheproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seededLibrary(t testing.TB, files bool) LibraryPaths {
	t.Helper()
	root := t.TempDir()
	paths := fixtureLibrary(root)
	store, err := CreateStore(paths.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec("UPDATE songs SET name='Song '||id, artist='Artist'"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.VideoDir, 0700); err != nil {
		t.Fatal(err)
	}
	if files {
		for i := 0; i < 10000; i++ {
			if err := os.WriteFile(filepath.Join(paths.VideoDir, fmt.Sprintf("%032x.mp4", i)), []byte("video"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return paths
}

// The public seam must not allocate a full catalog to return a single ID or page.
func TestLibraryPageAllocationBound(t *testing.T) {
	paths := seededLibrary(t, false)
	for _, q := range []LibraryQuery{{}, {Sort: "title"}, {Search: "id:2367"}, {View: "resources"}, {View: "resources", Search: "id:2367"}} {
		allocs := testing.AllocsPerRun(1, func() {
			page, err := ReadLibraryPage(t.Context(), paths, q)
			if err != nil || len(page.Songs) > 50 || len(page.Keys) > 50 {
				t.Fatal(page, err)
			}
		})
		if allocs > 20000 {
			t.Errorf("query %+v allocated %.0f objects for one page; want <20000", q, allocs)
		}
	}
}

func BenchmarkReadLibraryPageSeeded(b *testing.B) {
	for _, files := range []bool{false, true} {
		paths := seededLibrary(b, files)
		for _, tc := range []struct {
			name string
			q    LibraryQuery
		}{
			{"default", LibraryQuery{}},
			{"next", LibraryQuery{Offset: 50}},
			{"exact_id", LibraryQuery{Search: "id:2367"}},
			{"title", LibraryQuery{Sort: "title"}},
			{"score", LibraryQuery{Sort: "score"}},
			{"resources", LibraryQuery{View: "resources"}},
		} {
			b.Run(fmt.Sprintf("files_%t/%s", files, tc.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					page, err := ReadLibraryPage(b.Context(), paths, tc.q)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportMetric(float64(page.Total), "matches")
				}
			})
		}
	}
}

func BenchmarkReadLibraryPageMaintainedInventory(b *testing.B) {
	paths := seededLibrary(b, true)
	cfg := fixtureConfig()
	cfg.CacheUnlimited = true
	prepareFixtureResources(b, &cfg, filepath.Dir(paths.DatabasePath), false)
	engine, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { engine.Close() })
	for _, tc := range []struct {
		name string
		q    LibraryQuery
	}{
		{"default", LibraryQuery{}},
		{"next", LibraryQuery{Offset: 50}},
		{"exact_id", LibraryQuery{Search: "id:2367"}},
		{"title", LibraryQuery{Sort: "title"}},
		{"score", LibraryQuery{Sort: "score"}},
		{"resources", LibraryQuery{View: "resources"}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				page, err := engine.ReadLibraryPage(b.Context(), tc.q)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(page.Total), "matches")
			}
		})
	}
}

// BenchmarkReadLibraryPageExisting opts into read-only measurements of a real
// cache. Point SWD_BENCH_CACHE_ROOT at the directory containing videos
// and storage.sqlite. It never starts the engine or reads video contents.
func BenchmarkReadLibraryPageExisting(b *testing.B) {
	root := os.Getenv("SWD_BENCH_CACHE_ROOT")
	if root == "" {
		b.Skip("set SWD_BENCH_CACHE_ROOT to benchmark an existing cache")
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
		{"search_missing", "swd-benchmark-no-match-7c91", "title", 0},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				page, err := ReadLibraryPage(ctx, fixtureLibrary(root), LibraryQuery{View: "resources", Filter: "saved", Search: tc.query, Sort: tc.order, Offset: tc.offset})
				cancel()
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(page.Total), "matches")
				if len(page.Keys) > 50 {
					b.Fatalf("page exceeds limit: %d", len(page.Keys))
				}
			}
		})
	}
}
