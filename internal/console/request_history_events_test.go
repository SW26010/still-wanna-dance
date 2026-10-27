package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestHistoryEventsAdmissionAndReconnect(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		name, method, host, origin, site string
		code                             int
	}{
		{"initial", "GET", c.address, "", "same-origin", 200},
		{"reconnect", "GET", c.address, "http://" + c.address, "same-origin", 200},
		{"host", "GET", "evil.example", "", "", 403},
		{"origin", "GET", c.address, "http://evil.example", "", 403},
		{"site", "GET", c.address, "", "cross-site", 403},
		{"method", "HEAD", c.address, "", "same-origin", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/history/events", nil).WithContext(ctx)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Last-Event-ID", "999") // Always invalidate on reconnect.
			recorder := httptest.NewRecorder()
			w := &diagnosticEventWriter{ResponseRecorder: recorder, t: t, afterFlush: cancel}
			if tc.code == 200 {
				c.ServeHTTP(w, r)
			} else {
				c.ServeHTTP(recorder, r)
			}
			if w.Code != tc.code || ctx.Err() == context.DeadlineExceeded {
				t.Fatal(w.Code, w.Body.String(), ctx.Err())
			}
			if tc.code == 200 && (w.writes != 1 || w.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(w.Body.String(), "event: history\ndata: {\"closed\":false}")) {
				t.Fatal("missing initial invalidation", w.Body.String())
			}
		})
	}
	c.terms = termsReceipt{}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/history/events", nil))
	if w.Code != http.StatusPreconditionRequired {
		t.Fatal("stream bypassed terms", w.Code)
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("history subscription started the engine or gateway")
	}
}

func TestRequestHistoryEventsCloseAndDisconnect(t *testing.T) {
	for _, failure := range []string{"write", "flush", "cancel", "store-close"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t, writeError: failure == "write", flushError: failure == "flush"}
			w.afterFlush = func() {
				if failure == "store-close" {
					if err := c.store.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					cancel()
				}
			}
			c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/history/events", nil).WithContext(ctx))
			want := 1
			if failure == "store-close" {
				want = 2
			}
			if ctx.Err() == context.DeadlineExceeded || w.writes != want {
				t.Fatal("stream did not stop", w.writes, ctx.Err())
			}
			if failure == "store-close" && !strings.Contains(w.Body.String(), `"closed":true`) {
				t.Fatal("missing terminal event", w.Body.String())
			}
		})
	}
}
