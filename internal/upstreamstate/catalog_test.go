package upstreamstate

import (
	"context"
	"net/http"
	"testing"
)

func TestCatalogDeliveryReusesAllProbeResponses(t *testing.T) {
	for _, mode := range []string{"ordinary", "candidates"} {
		t.Run(mode, func(t *testing.T) {
			var m *Monitor
			var requests int
			var f *candidateFixture
			want := 3
			if mode == "candidates" {
				m, f = candidateMonitor(t)
				want = 6
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
					if r.Source != entry(Catalog, r.Route) || len(r.Body) == 0 {
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
			if calls != 1 || requests != want {
				t.Fatalf("deliveries=%d, requests=%d, want %d", calls, requests, want)
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

func TestPlaybackPrerequisiteDeliversCatalogOnlyWhenFetched(t *testing.T) {
	m := testMonitor(t, fixture)
	calls := 0
	m.onCatalog = func(_ context.Context, responses []CatalogResponse) {
		calls++
		if len(responses) != 1 || responses[0].Route != "api" {
			t.Errorf("unexpected prerequisites: %+v", responses)
		}
	}
	for i := 0; i < 2; i++ {
		if err := m.CheckSelected(context.Background(), CheckPlayback); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("cached sample fetched catalog again: %d", calls)
	}
}
