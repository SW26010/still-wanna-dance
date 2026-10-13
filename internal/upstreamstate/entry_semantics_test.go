package upstreamstate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Characterizes the current observation contract before a B2 integration:
// GotConn is an entry-stage event, not proof of a healthy remote connection.
// Local admission/retirement errors currently have no separate health class.
func TestEntryFailureFeedbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		err         error
	}{
		{"peer-eof", "network_error", io.EOF},
		{"local-capacity", "network_error", errors.New("dial-capacity")},
		{"retired", "network_error", upstreamrequest.ErrUnavailable},
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
				if calls != 1 || o.state != tc.state || o.stage != stage || o.latency != 0 {
					t.Fatalf("calls=%d observation=%+v", calls, o)
				}
				m := testMonitor(t, fixture)
				m.mu.Lock()
				prior := observation{op: Catalog, route: "api", state: "available", at: o.at.Add(-time.Second), duration: time.Millisecond, latency: time.Millisecond, bytes: 10}
				m.record(prior)
				m.record(o)
				result := m.resultLocked(Catalog, "api", time.Now())
				m.mu.Unlock()
				if tc.state == "canceled" {
					if result.State != "available" {
						t.Fatal("caller cancellation replaced good observation", result)
					}
				} else if result.State != "unavailable" || result.Reason != tc.state || result.EstimatedLatencyMS != nil {
					t.Fatal("failure became latency success or changed reason", result)
				}
			})
		}
	}
}
