package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func completeCatalogStore(b *testing.B) (*Store, Catalog) {
	b.Helper()
	store, err := createEmptyFixtureStore(filepath.Join(b.TempDir(), "storage.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Error(err)
		}
	})
	c := Catalog{Source: KivaCatalogURL, Revision: "20261115000000", Songs: make([]CatalogSong, 10575)}
	for i := range c.Songs {
		c.Songs[i] = CatalogSong{ID: int64(i + 1), MD5: fmt.Sprintf("%032x", i+1)}
	}
	if err := store.SyncCatalog(b.Context(), c); err != nil {
		b.Fatal(err)
	}
	return store, c
}

func BenchmarkSyncCatalogComplete(b *testing.B) {
	for _, older := range []bool{false, true} {
		b.Run(fmt.Sprintf("older_%t", older), func(b *testing.B) {
			store, c := completeCatalogStore(b)
			var wantErr error
			if older {
				c.Revision, wantErr = "20261114000000", ErrCatalogOlder
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.SyncCatalog(b.Context(), c); !errors.Is(err, wantErr) {
					b.Fatal(err)
				}
			}
		})
	}
}

// Pause SyncCatalog at connection acquisition, after it holds the mapping and
// retention locks. A real favorite write then competes for that same exclusion.
// Normalization and fixture setup are excluded from favorite_wait_ns/op.
func BenchmarkSyncCatalogFavoriteWait(b *testing.B) {
	store, c := completeCatalogStore(b)
	store.db.SetMaxOpenConns(1)
	var waited time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ctx, cancel := context.WithTimeout(b.Context(), 10*time.Second)
		conn, err := store.db.Conn(ctx)
		if err != nil {
			cancel()
			b.Fatal(err)
		}
		before := store.db.Stats().WaitCount
		synced := make(chan error, 1)
		go func() { synced <- store.SyncCatalog(ctx, c) }()
		for store.db.Stats().WaitCount == before && ctx.Err() == nil {
			runtime.Gosched()
		}
		if err := ctx.Err(); err != nil {
			conn.Close()
			cancel()
			b.Fatal(err)
		}
		favorited := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			close(started)
			favorited <- store.SetSongPolicy(ctx, 1, SongFavorite)
		}()
		<-started
		b.StartTimer()
		start := time.Now()
		if err := conn.Close(); err != nil {
			cancel()
			b.Fatal(err)
		}
		if err := <-favorited; err != nil {
			cancel()
			b.Fatal(err)
		}
		waited += time.Since(start)
		if err := <-synced; err != nil {
			cancel()
			b.Fatal(err)
		}
		b.StopTimer()
		cancel()
	}
	b.ReportMetric(float64(waited.Nanoseconds())/float64(b.N), "favorite_wait_ns/op")
}
