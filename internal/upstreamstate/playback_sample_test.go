package upstreamstate

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
)

func TestPlaybackSampleUsesPriorityPassAndFallsBack(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		name, requestsPerNode := "ordinary", 1
		if candidates {
			name, requestsPerNode = "candidates", 2
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var requested []int64
			catalogRequests := 0
			m := playbackDeliveryMonitor(t, candidates, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/Api/Songs/play" {
					id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
					mu.Lock()
					requested = append(requested, id)
					mu.Unlock()
					// Failure must let the next missing song advance next batch.
					return response(http.StatusServiceUnavailable, ""), nil
				}
				mu.Lock()
				catalogRequests++
				mu.Unlock()
				return fixture(r)
			}, nil)
			pending := []int64{73, 91}
			m.playbackSongCandidates = func(context.Context) []int64 {
				m.Snapshot() // Never under the monitor lock.
				return slices.Clone(pending)
			}
			for round, want := range []int64{73, 91, 73, 42} {
				if round == 3 {
					pending = nil
				}
				requested = nil
				if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
					t.Fatal(err)
				}
				if len(requested) != requestsPerNode*2 {
					t.Fatal("changed playback request count", requested)
				}
				for _, id := range requested {
					if id != want {
						t.Fatalf("round %d sampled %d, want %d", round, id, want)
					}
				}
			}
			if catalogRequests != requestsPerNode {
				t.Fatal("sample selection added catalog requests", catalogRequests)
			}
		})
	}
}

func TestPlaybackSampleSelectionAfterCatalogAndWithoutRemoteSample(t *testing.T) {
	for _, candidates := range []bool{false, true} {
		for _, catalogOK := range []bool{false, true} {
			ready := !catalogOK // Existing local data can seed a failed catalog round.
			var mu sync.Mutex
			var requested []int64
			m := playbackDeliveryMonitor(t, candidates, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/Api/Songs/play" {
					id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
					mu.Lock()
					requested = append(requested, id)
					mu.Unlock()
					return response(http.StatusServiceUnavailable, ""), nil
				}
				if !catalogOK {
					return response(http.StatusServiceUnavailable, ""), nil
				}
				return fixture(r)
			}, nil)
			m.onCatalog = func(context.Context, []CatalogResponse) { ready = true }
			m.playbackSongCandidates = func(context.Context) []int64 {
				if !ready {
					t.Error("selected before catalog sync")
				}
				return []int64{73}
			}
			if err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(requested) == 0 {
				t.Fatal("local candidate was not probed")
			}
			for _, id := range requested {
				if id != 73 {
					t.Fatal("ignored local priority", requested)
				}
			}
		}
	}
}

func TestPlaybackSampleSelectionCancellationAndCatalogOnly(t *testing.T) {
	m := testMonitor(t, fixture)
	called := 0
	m.playbackSongCandidates = func(context.Context) []int64 { called++; return []int64{73} }
	revision := m.channel.Snapshot().Revision
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.selectPlaybackSong(ctx, revision, CheckPlayback, 42) != 0 || m.selectPlaybackSong(context.Background(), revision+1, CheckPlayback, 42) != 0 {
		t.Fatal("canceled/retired selection accepted")
	}
	if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil || called != 0 {
		t.Fatal("catalog-only check selected playback", called, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	m.playbackSongCandidates = func(context.Context) []int64 { cancel(); return []int64{73} }
	if m.selectPlaybackSong(ctx, revision, CheckPlayback, 42) != 0 || len(m.playbackSongsAttempted) != 0 {
		t.Fatal("cancellation during selection consumed a candidate")
	}
}
