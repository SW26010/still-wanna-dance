package console

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadsAuthAndOffline(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		host, token, origin string
		code                int
	}{
		{c.address, "", "", 403}, {"evil.example", c.token, "", 403},
		{c.address, c.token, "http://evil.example", 403}, {c.address, c.token, "", 200},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/downloads", nil)
		r.Header.Set("X-StepStash-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Code == 200 && (!strings.Contains(w.Body.String(), `"tasks":[]`) || !strings.Contains(w.Body.String(), `"running":false`)) {
			t.Fatal(w.Body.String())
		}
	}
	if c.service != nil {
		t.Fatal("read started engine")
	}
}
