package upstreamstate

import (
	"context"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectivityUsesMonitorRoutesCallbacksAndOneByteProbes(t *testing.T) {
	for _, mode := range []string{"ordinary", "candidates"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var m *Monitor
			var f *candidateFixture
			wantRequests, wantCatalogs, wantURLs := 7, 2, 2
			if mode == "candidates" {
				m, f = candidateMonitor(t)
				wantRequests, wantCatalogs, wantURLs = 14, 4, 4
			} else {
				m = testMonitor(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					if value := r.Header.Get("Range"); value != "" && value != "bytes=0-0" {
						t.Error("connectivity requested throughput", value)
					}
					return fixture(r)
				})
			}
			var phases []Operation
			catalogs := 0
			var urls atomic.Int32
			m.onCatalog = func(ctx context.Context, responses []CatalogResponse) {
				m.Snapshot() // Callbacks run outside the Monitor lock.
				catalogs += len(responses)
				for _, response := range responses {
					if response.Route == "api" || len(response.Body) == 0 {
						t.Error("unexpected business catalog", response.Route)
					}
				}
			}
			m.onPlaybackURL = func(context.Context, PlaybackURLResponse) { urls.Add(1) }
			m.onProgress = func(ctx context.Context, p CheckProgress) {
				m.Snapshot()
				phases = append(phases, p.Operation)
			}
			// A second run must check all catalogs even with a cached song sample.
			for round := 1; round <= 2; round++ {
				phases = nil
				if err := m.CheckSelected(context.Background(), CheckConnectivity); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(phases, []Operation{Catalog, PlaybackURL, Resource, ""}) {
					t.Fatal("phase ordering", phases)
				}
				requests := int(calls.Load())
				if f != nil {
					f.mu.Lock()
					requests = f.calls
					f.mu.Unlock()
				}
				if requests != round*wantRequests || catalogs != round*wantCatalogs || int(urls.Load()) != round*wantURLs {
					t.Fatalf("requests=%d catalogs=%d urls=%d", requests, catalogs, urls.Load())
				}
				if !m.Snapshot().LastThroughputAttempt.IsZero() {
					t.Fatal("connectivity reset throughput cooldown")
				}
				for _, r := range m.Snapshot().Results {
					if r.State != "available" || r.EstimatedLatencyMS == nil {
						t.Fatal("missing Monitor observation", r)
					}
					if r.Operation == Resource && (r.EstimatedSpeedBPS != nil || !r.ThroughputObservedAt.IsZero()) {
						t.Fatal("connectivity recorded throughput", r)
					}
				}
			}
		})
	}
}

func TestConnectivityMissingCatalogDoesNotStartLaterPhases(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return response(503, ""), nil })
	var phases []Operation
	m.onProgress = func(_ context.Context, p CheckProgress) { phases = append(phases, p.Operation) }
	if err := m.CheckSelected(context.Background(), CheckConnectivity); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(phases, []Operation{Catalog, ""}) {
		t.Fatal(phases)
	}
}

func TestConnectivityCloseCancelsAndDrainsProgress(t *testing.T) {
	m := testMonitor(t, fixture)
	entered, drained := make(chan struct{}), make(chan struct{})
	m.onProgress = func(ctx context.Context, p CheckProgress) {
		if p.Operation != Catalog {
			t.Error("published after cancellation", p.Operation)
			return
		}
		close(entered)
		<-ctx.Done()
		close(drained)
	}
	done := make(chan error, 1)
	go func() { done <- m.CheckSelected(context.Background(), CheckConnectivity) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("progress did not start")
	}
	m.Close()
	select {
	case <-drained:
	default:
		t.Fatal("Close did not drain callback")
	}
	<-done
}
