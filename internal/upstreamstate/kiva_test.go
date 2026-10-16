package upstreamstate

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestKivaCatalogValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", kivaFixture, true},
		{"uppercase MD5", strings.ReplaceAll(kivaFixture, "abcdef", "ABCDEF"), true},
		{"business error", strings.Replace(kivaFixture, "200", "500", 1), false},
		{"missing revision", strings.Replace(kivaFixture, "20261005000000", "", 1), false},
		{"invalid ID", strings.Replace(kivaFixture, "42", "0", 1), false},
		{"invalid MD5", strings.Replace(kivaFixture, "0123456789abcdef0123456789abcdef", "invalid", 1), false},
		{"empty", `{"code":200,"data":{"time":"now","groups":[]}}`, false},
		{"wrong schema", catalogFixture, false},
		{"conflicting MD5", `{"code":200,"data":{"time":"now","groups":[{"entries":[{"id":42,"checksum":"0123456789abcdef0123456789abcdef"},{"id":42,"checksum":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://x.kiva.moe/api/v2/wanna/songs" {
					t.Fatal(r.URL)
				}
				return response(200, tc.body), nil
			}))
			o, id := probeCatalogRoute(context.Background(), client, DefaultPolicy(), "kiva")
			if o.route != "kiva" || o.op != Catalog {
				t.Fatal(o)
			}
			if tc.valid {
				if o.state != "available" || id != 42 || o.bytes == 0 {
					t.Fatal(o, id)
				}
			} else if o.state != "invalid" || id != 0 {
				t.Fatal(o, id)
			}
		})
	}
}

func TestCatalogBaseValidationAgreesWithImport(t *testing.T) {
	entry := `{"id":42,"checksum":"0123456789abcdef0123456789abcdef"}`
	for _, tc := range []struct {
		name, body       string
		sample, imported bool
	}{
		{"valid", kivaFixture, true, true},
		{"same ID and checksum", strings.Replace(kivaFixture, entry, entry+","+entry, 1), false, false},
		{"invalid calendar", strings.Replace(kivaFixture, "20261005000000", "20260230000000", 1), false, false},
		{"missing entries", strings.Replace(kivaFixture, "entries", "missing", 1), false, false},
		{"shared content", strings.Replace(kivaFixture, entry, entry+","+strings.Replace(entry, "42", "43", 1), 1), true, true},
		{"optional metadata", strings.Replace(kivaFixture, entry, strings.Replace(entry, `"id":42`, `"id":42,"volume":"invalid"`, 1), 1), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, _ := kivaSongSample([]byte(tc.body))
			_, err := cacheproxy.ParseCatalog([]byte(tc.body), "test")
			if (id != 0) != tc.sample || (err == nil) != tc.imported {
				t.Fatalf("sample=%d import=%v", id, err)
			}
		})
	}
}

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
			o, id := probeCatalogRoute(context.Background(), client, DefaultPolicy(), tc.route)
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
