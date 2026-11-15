package upstreamcheck

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Exercise the parsing stages of a successful probe, selection and sync,
// without including network latency or database writes in the measurement.
func BenchmarkCatalogDetectionParsing(b *testing.B) {
	for _, route := range []string{"api", "kiva"} {
		b.Run(route, func(b *testing.B) {
			var body strings.Builder
			if route == "api" {
				body.WriteString(`{"time":"20261115000000","groups":{"contents":[{"groupName":"group","major":"major","songInfos":[`)
			} else {
				body.WriteString(`{"code":200,"data":{"time":"20261115000000","groups":[{"title":"group","major":"major","entries":[`)
			}
			for i := 1; i <= 10575; i++ {
				if i > 1 {
					body.WriteByte(',')
				}
				fmt.Fprintf(&body, `{"id":%d,"name":"synthetic song","artist":"artist","dancer":"dancer","playerCount":2,"volume":0.35,"start":0,"end":180,"flip":false,"rpe":5,"tag":["tag"],"checksum":"0123456789abcdef0123456789abcdef"`, i)
				if route == "api" {
					body.WriteString(`,"double_width":false,"skip_random":0,"disable_public":0,"shader_motion":[]}`)
				} else {
					body.WriteString(`,"doubleWidth":false,"skipRandom":false,"disablePublic":false,"shaderMotion":[]}`)
				}
			}
			body.WriteString(`]}]}}`)
			fixture := body.String()
			client := requestClient(transportFunc(func(*http.Request) (*http.Response, error) {
				return response(200, fixture), nil
			}))
			b.SetBytes(int64(len(fixture)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				o, id, result := probeCatalogDataAt(context.Background(), client, DefaultPolicy(), route, entry(Catalog, route))
				if o.state != "available" || id == 0 {
					b.Fatal(o, id)
				}
				if _, err := result.Parse(); err != nil {
					b.Fatal(err)
				}
				if catalog, err := result.Parse(); err != nil || len(catalog.Songs) != 10575 {
					b.Fatal(len(catalog.Songs), err)
				}
			}
		})
	}
}

func TestCatalogResponseReusesValidation(t *testing.T) {
	r := CatalogResponse{Source: "test", Body: []byte(catalogFixture)}
	first, err := r.Parse()
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Parse()
	if err != nil || &first.Songs[0] != &second.Songs[0] {
		t.Fatal("catalog was parsed again", err)
	}
	bad := CatalogResponse{Source: "test", Body: []byte(`{}`)}
	_, firstErr := bad.Parse()
	_, secondErr := bad.Parse()
	if firstErr == nil || firstErr != secondErr {
		t.Fatal("validation failure was parsed again", firstErr, secondErr)
	}
}

func TestConnectivityReusesInvalidCatalogValidation(t *testing.T) {
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		// Valid envelopes/identities still produce a network observation, while
		// invalid optional metadata is rejected by selection and reported once.
		body := catalogFixture
		if r.URL.Path != "/Api/Songs/list" {
			body = kivaFixture
		}
		return response(200, strings.Replace(body, `"id":42`, `"id":42,"volume":"bad"`, 1)), nil
	}))
	writes := 0
	CheckConnectivity(t.Context(), ConnectivityAccess{
		Catalog: func(ctx context.Context, route string) (Result, int64, CatalogResponse) {
			return ProbeCatalog(ctx, []*http.Client{client}, DefaultPolicy(), route)
		},
	}, ConnectivityBusiness{
		OnCatalog: func(_ context.Context, responses []CatalogResponse) (bool, error) {
			writes++
			if len(responses) != 4 {
				t.Fatal("lost invalid catalog feedback", len(responses))
			}
			for _, r := range responses {
				if r.parsed == nil || r.parseErr == nil {
					t.Fatal("validation failure was not passed to business")
				}
				if _, err := r.Parse(); err != r.parseErr {
					t.Fatal("invalid catalog was parsed again", err)
				}
			}
			return false, nil
		},
	}, Catalog, func(Result) {})
	if writes != 1 {
		t.Fatal("expected one business feedback call", writes)
	}
}
