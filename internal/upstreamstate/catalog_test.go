package upstreamstate

import (
	"context"
	"net/http"
	"testing"
)

func TestCatalogDeliveryReusesOnlyMD5ProbeResponses(t *testing.T) {
	for _, mode := range []string{"ordinary", "candidates"} {
		t.Run(mode, func(t *testing.T) {
			var m *Monitor
			var requests int
			var f *candidateFixture
			want, wantRequests := 2, 3
			if mode == "candidates" {
				m, f = candidateMonitor(t)
				want, wantRequests = 4, 6
			} else {
				m = testMonitor(t, func(r *http.Request) (*http.Response, error) { requests++; return fixture(r) })
			}
			calls := 0
			m.onCatalog = func(ctx context.Context, responses []CatalogResponse) {
				calls++
				m.Snapshot() // Callback cannot hold the monitor lock.
				if ctx.Err() != nil || len(responses) != want {
					t.Errorf("responses=%d, error=%v", len(responses), ctx.Err())
				}
				for _, r := range responses {
					if r.Route == "api" || r.Source != entry(Catalog, r.Route) || len(r.Body) == 0 {
						t.Errorf("bad response: %+v", r)
					}
				}
			}
			if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil {
				t.Fatal(err)
			}
			if f != nil {
				f.mu.Lock()
				requests = f.calls
				f.mu.Unlock()
			}
			if calls != 1 || requests != wantRequests {
				t.Fatalf("deliveries=%d, requests=%d, want %d", calls, requests, wantRequests)
			}
		})
	}
}

func TestCatalogDeliveryRejectsCanceledAndRetiredBatches(t *testing.T) {
	m := testMonitor(t, fixture)
	m.onCatalog = func(context.Context, []CatalogResponse) { t.Error("invalid batch delivered") }
	r := []CatalogResponse{{Body: []byte(catalogFixture)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.deliverCatalogs(ctx, m.channel.Snapshot().Revision, r)
	m.deliverCatalogs(context.Background(), m.channel.Snapshot().Revision+1, r)
}

func TestInvalidProbeDoesNotDeliverCatalog(t *testing.T) {
	m := testMonitor(t, func(*http.Request) (*http.Response, error) { return response(200, `{}`), nil })
	m.onCatalog = func(context.Context, []CatalogResponse) { t.Error("invalid response delivered") }
	if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil {
		t.Fatal(err)
	}
}

func TestPlaybackUdonPrerequisiteStaysInsideMonitor(t *testing.T) {
	requests := 0
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/list" {
			requests++
		}
		return fixture(r)
	})
	m.onCatalog = func(context.Context, []CatalogResponse) { t.Error("Udon delivered to business consumer") }
	for i := 0; i < 2; i++ {
		if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatal("monitor did not fetch/reuse Udon sample", requests)
	}
}
