package upstreamstate

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestShortDNSLeaseDoesNotRepeatHealthyChecks(t *testing.T) {
	for _, lease := range []time.Duration{time.Hour, 45 * time.Second} {
		t.Run(lease.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, candidates := candidateMonitor(t)
				candidates.expires = time.Now().Add(lease)
				var catalogs, playback atomic.Int32
				m.onCatalog = func(context.Context, []CatalogResponse) { catalogs.Add(1) }
				m.onPlaybackURL = func(context.Context, PlaybackURLResponse) { playback.Add(1) }
				m.lastThroughput = time.Now()
				if err := m.Start(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				first := playback.Load()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				if catalogs.Load() != 1 || playback.Load() != first {
					t.Fatalf("unchanged DNS candidates repeated healthy checks: catalogs=%d playback=%d (initial %d)", catalogs.Load(), playback.Load(), first)
				}
				m.Close()
			})
		})
	}
}
