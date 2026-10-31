package upstreamstate

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestMonitorChecksOnlyUdonCatalog(t *testing.T) {
	for _, failed := range []bool{false, true} {
		m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "x.kiva.moe" || r.URL.Host == "wanna.kiva.moe" {
				t.Fatal("optional catalog requested by optimization", r.URL)
			}
			if failed && r.URL.Path == "/Api/Songs/list" {
				return response(503, ""), nil
			}
			return fixture(r)
		})
		if err := m.CheckSelected(context.Background(), CheckCatalog); err != nil {
			t.Fatal(err)
		}
		rows := m.Results(Catalog)
		want := "available"
		if failed {
			want = "unavailable"
		}
		if len(rows) != 1 || rows[0].Route != "api" || rows[0].State != want {
			t.Fatal(rows)
		}
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
			if tc.route != "api" {
				return
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
