package upstreamstate

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogSourcesFailIndependently(t *testing.T) {
	for _, failed := range []string{"api.udon.dance", "x.kiva.moe", "wanna.kiva.moe"} {
		t.Run(failed, func(t *testing.T) {
			m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == failed && r.URL.Path != "/Api/Songs/play" {
					return response(503, ""), nil
				}
				return fixture(r)
			})
			if err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, route := range routeIDs(Catalog) {
				r := resultFor(t, m, Catalog, route)
				want := "available"
				if strings.Contains(r.Entry, failed) {
					want = "unavailable"
				}
				if r.State != want {
					t.Fatalf("%+v", r)
				}
			}
			for _, r := range m.Results(Resource) {
				if r.State != "available" || r.SampleSongID != 42 {
					t.Fatalf("healthy catalog did not seed video probes: %+v", r)
				}
			}
		})
	}
}

func TestCatalogResponseTimeIsIndependentOfObservationTime(t *testing.T) {
	for _, tc := range []struct{ route, body, want string }{
		{"api", catalogFixture, "20261004235822"},
		{"kiva", kivaFixture, "20261005000000"},
		{"wanna", strings.Replace(kivaFixture, "20261005000000", "20260215004959", 1), "20260215004959"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != entry(Catalog, tc.route) {
					t.Fatal(r.URL)
				}
				return response(200, tc.body), nil
			}))
			o, id, _ := probeCatalogResponse(context.Background(), client, DefaultPolicy(), tc.route)
			if o.state != "available" || id != 42 || o.catalogTime != tc.want || o.at.IsZero() {
				t.Fatal(o, id)
			}
			m := testMonitor(t, transportFunc(fixture))
			m.mu.Lock()
			recordFixture(m, o)
			m.mu.Unlock()
			r := resultFor(t, m, Catalog, tc.route)
			if r.CatalogTime != tc.want || !r.ObservedAt.Equal(o.at) {
				t.Fatal(r)
			}
			// A newer failed response must not inherit an older response's time.
			o.catalogTime, o.state = "", "network_error"
			m.mu.Lock()
			recordFixture(m, o)
			m.mu.Unlock()
			if r := resultFor(t, m, Catalog, tc.route); r.CatalogTime != "" {
				t.Fatal(r)
			}
		})
	}
}

func TestWannaInfoCanSeedResourcesWhenOtherCatalogsFail(t *testing.T) {
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/Api/Songs/list" || r.URL.Host == "x.kiva.moe" {
			return response(503, ""), nil
		}
		return fixture(r)
	})
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := resultFor(t, m, Catalog, "wanna"); r.State != "available" || r.CatalogTime != "20261005000000" {
		t.Fatal(r)
	}
	for _, r := range m.Results(Resource) {
		if r.State != "available" || r.SampleSongID != 42 {
			t.Fatal(r)
		}
	}
}
