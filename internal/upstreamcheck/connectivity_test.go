package upstreamcheck

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
)

func TestConnectivityCatalogFallbackStopsAfterAcceptance(t *testing.T) {
	for _, accepted := range []string{"api", "kiva", "wanna", "none", "local-error"} {
		t.Run(accepted, func(t *testing.T) {
			var requested, delivered []string
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				want := "https"
				if r.URL.Host == "api.udon.dance" {
					want = "http"
				}
				if r.URL.Scheme != want {
					t.Fatal("wrong source protocol", r.URL)
				}
				return fixture(r)
			}))
			access := ConnectivityAccess{Catalog: func(ctx context.Context, route string) (Result, int64, CatalogResponse) {
				requested = append(requested, route)
				return ProbeCatalog(ctx, []*http.Client{client}, DefaultPolicy(), route)
			}}
			business := ConnectivityBusiness{OnCatalog: func(_ context.Context, responses []CatalogResponse) (bool, error) {
				if len(responses) != 1 {
					t.Fatal(responses)
				}
				route := responses[0].Route
				delivered = append(delivered, route)
				if accepted == "local-error" {
					return false, errors.New("local storage unavailable")
				}
				return route == accepted, nil
			}}
			var skipped []string
			CheckConnectivity(t.Context(), access, business, true, func(r Result) {
				if r.State == "skipped" {
					if !r.ObservedAt.IsZero() || r.Responded {
						t.Fatal("skipped source fabricated an observation", r)
					}
					skipped = append(skipped, r.Route)
				}
			})
			want := []string{"api", "kiva", "wanna"}
			if accepted == "api" || accepted == "local-error" {
				want = want[:1]
			} else if accepted == "kiva" {
				want = want[:2]
			}
			if !slices.Equal(requested, want) || !slices.Equal(delivered, want) {
				t.Fatal(requested, delivered, want)
			}
			if accepted != "local-error" && len(skipped) != 3-len(want) {
				t.Fatal(skipped)
			}
		})
	}
}
