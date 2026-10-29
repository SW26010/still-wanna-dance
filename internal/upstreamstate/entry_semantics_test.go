package upstreamstate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// GotConn is an entry-stage event, not proof of a healthy remote connection.
// Local execution failures preserve remote quality and remain diagnostic.
func TestEntryFailureFeedbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		err         error
	}{
		{"peer-eof", "network_error", io.EOF},
		{"local-capacity", "local_capacity", upstreamrequest.ErrCapacity},
		{"retired", "runtime_retired", upstreamrequest.ErrRetired},
		{"channel-unavailable", "channel_unavailable", upstreamrequest.ErrUnavailable},
		{"request-cancel", "canceled", context.Canceled},
		{"request-deadline", "timeout", context.DeadlineExceeded},
	} {
		for _, gotConn := range []bool{false, true} {
			stage := "connect"
			if gotConn {
				stage = "headers"
			}
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				calls := 0
				client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if gotConn {
						httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{})
					}
					return nil, tc.err
				}))
				o, _ := probeCatalog(context.Background(), client, DefaultPolicy())
				wantCalls := 1
				if tc.name == "peer-eof" || tc.name == "request-deadline" {
					wantCalls = 2 // HTTPS transport failure permits one HTTP attempt.
				}
				if calls != wantCalls || o.state != tc.state || o.stage != stage || o.latency != 0 {
					t.Fatalf("calls=%d observation=%+v", calls, o)
				}
				m := testMonitor(t, fixture)
				m.mu.Lock()
				prior := observation{op: Catalog, route: "api", state: "available", at: o.at.Add(-time.Second), duration: time.Millisecond, latency: time.Millisecond, bytes: 10}
				recordFixture(m, prior)
				recordFixture(m, o)
				result := fixtureResult(m, Catalog, "api", time.Now())
				m.mu.Unlock()
				if localObservation(tc.state) {
					if result.State != "available" || result.Samples != 1 || result.LocalDiagnostic != tc.state {
						t.Fatal("caller cancellation replaced good observation", result)
					}
				} else if result.State != "unavailable" || result.Reason != tc.state || result.EstimatedLatencyMS != nil {
					t.Fatal("failure became latency success or changed reason", result)
				}
			})
		}
	}
}
