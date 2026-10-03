package console

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestExitAuthenticationAndAcknowledgement(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct{ method, host, token, origin string }{
		{"GET", c.address, c.token, ""},
		{"POST", "evil.invalid", c.token, ""},
		{"POST", c.address, "", ""},
		{"POST", c.address, c.token, "https://evil.invalid"},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/exit", nil)
		r.Header.Set("X-StepStash-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("invalid exit accepted: %+v", tc)
		}
		select {
		case <-c.ExitRequested():
			t.Fatal("invalid request signaled exit")
		default:
		}
	}
	// Cleanup remains available without terms acceptance and is idempotent.
	for range 2 {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/exit", nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 || !w.Flushed || w.Body.String() != "{\"ok\":true}\n" {
			t.Fatalf("exit acknowledgement: %d %q flushed=%v", w.Code, w.Body.String(), w.Flushed)
		}
		select {
		case <-c.ExitRequested():
		default:
			t.Fatal("exit not signaled")
		}
	}
}
