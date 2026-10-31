package console

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type slowCheckDiscovery struct{ http.RoundTripper }

func (s slowCheckDiscovery) Current(string) []upstreamrequest.Candidate { return nil }
func (s slowCheckDiscovery) Candidates(ctx context.Context, _ string) ([]upstreamrequest.Candidate, error) {
	time.Sleep(20 * time.Second)
	return []upstreamrequest.Candidate{{ID: "direct/test/127.0.0.1", Mode: "direct", Transport: s.RoundTripper}}, ctx.Err()
}
func (s slowCheckDiscovery) Changed() <-chan struct{} { return nil }

func TestConnectivityDiscoverySharesRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var requests int
		c := &Console{client: &http.Client{Transport: slowCheckDiscovery{catalogTransport(func(r *http.Request) (*http.Response, error) {
			requests++
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}}}
		c.settings.UpstreamMode = "direct"
		started := time.Now()
		result, _, _ := c.connectivityAccess().Catalog(t.Context(), "api")
		if result.State != "timeout" || requests != 1 || time.Since(started) != 30*time.Second {
			t.Fatalf("discovery and HTTP request received separate budgets: result=%+v requests=%d elapsed=%v", result, requests, time.Since(started))
		}
	})
}
