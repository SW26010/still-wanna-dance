package upstreamcheck

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogmeta"
)

func TestConnectivityCatalogParallelSelection(t *testing.T) {
	for _, scenario := range []string{"primary", "backup-first", "primary-failed", "primary-invalid", "backup-tie", "all-failed", "local-error", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var started, finished, canceled atomic.Int32
				client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
					started.Add(1)
					defer finished.Add(1)
					delay := 4 * time.Second
					switch r.URL.String() {
					case catalogmeta.UdonURL:
						delay = 2 * time.Second
					case catalogmeta.UdonHTTPURL:
						if scenario == "backup-first" {
							delay = time.Second
						}
					case catalogmeta.KivaURL:
						delay = 3 * time.Second
					}
					select {
					case <-time.After(delay):
					case <-r.Context().Done():
						canceled.Add(1)
						return nil, r.Context().Err()
					}
					if scenario == "all-failed" || r.URL.String() == catalogmeta.UdonURL && (scenario == "primary-failed" || scenario == "backup-tie") {
						return response(503, ""), nil
					}
					if scenario == "primary-invalid" && r.URL.String() == catalogmeta.UdonURL {
						return response(200, strings.Replace(catalogFixture, `"id":42`, `"id":42,"playerCount":"bad"`, 1)), nil
					}
					if scenario == "backup-tie" && r.URL.String() == catalogmeta.UdonHTTPURL {
						return response(200, strings.Replace(catalogFixture, "20261004235822", "20261005000000", 1)), nil
					}
					return fixture(r)
				}))
				access := ConnectivityAccess{Catalog: func(ctx context.Context, route string) (Result, int64, CatalogResponse) {
					return ProbeCatalog(ctx, []*http.Client{client}, DefaultPolicy(), route)
				}}
				var acceptedAt time.Duration
				var acceptedSource string
				var writes int
				business := ConnectivityBusiness{OnCatalog: func(_ context.Context, responses []CatalogResponse) (bool, error) {
					writes++
					if len(responses) != 1 {
						t.Fatal("only the winner may be committed", responses)
					}
					acceptedAt, acceptedSource = time.Since(start), responses[0].Source
					if scenario == "local-error" {
						return false, cacheproxy.ErrLocalStorage
					}
					return true, nil
				}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if scenario == "cancel" {
					time.AfterFunc(time.Second, cancel)
				}
				var selected []Result
				rows := map[string]Result{}
				CheckConnectivity(ctx, access, business, Catalog, func(r Result) {
					rows[r.Route] = r
					if r.Selected {
						selected = append(selected, r)
					}
				})
				if started.Load() != 4 || finished.Load() != 4 {
					t.Fatal("requests were not concurrent or drained", started.Load(), finished.Load())
				}
				if scenario == "cancel" {
					if writes != 0 || canceled.Load() != 4 {
						t.Fatal(writes, canceled.Load())
					}
					return
				}
				if scenario == "local-error" {
					if writes != 1 || canceled.Load() != 3 || len(selected) != 0 {
						t.Fatal(writes, canceled.Load(), selected)
					}
					return
				}
				if time.Since(start) != 4*time.Second || canceled.Load() != 0 {
					t.Fatal("remaining requests must finish naturally", time.Since(start), canceled.Load())
				}
				if scenario == "all-failed" {
					if writes != 0 || len(selected) != 0 {
						t.Fatal(writes, selected)
					}
					return
				}
				wantSource, wantTime := catalogmeta.UdonURL, 2*time.Second
				if scenario == "primary-failed" || scenario == "primary-invalid" {
					wantSource, wantTime = catalogmeta.KivaURL, 4*time.Second
				}
				if scenario == "backup-tie" {
					wantSource, wantTime = catalogmeta.UdonHTTPURL, 4*time.Second
				}
				if writes != 1 || acceptedAt != wantTime || acceptedSource != wantSource || len(selected) != 1 {
					t.Fatal(writes, acceptedAt, acceptedSource, selected)
				}
				if catalogmeta.SourceURL(selected[0].Route) != wantSource || selected[0].State != "available" {
					t.Fatal(selected)
				}
				for _, r := range rows {
					if r.State == "checking" || r.State == "skipped" {
						t.Fatal("lost completed observation", r)
					}
				}
				if scenario == "primary-invalid" && rows["api"].State != "invalid" {
					t.Fatal("full metadata was not validated", rows)
				}
			})
		})
	}
}
