package console

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLibraryAPIReadOnlyAndValidation(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		path, token, origin string
		want                int
	}{
		{"/api/library", c.token, "", 200},
		{"/api/library?view=resources&sort=recent", c.token, "", 200},
		{"/api/library", "", "", 403},
		{"/api/library", c.token, "http://evil.example", 403},
		{"/api/library?view=no", c.token, "", 400},
		{"/api/library?offset=-1", c.token, "", 400},
		{"/api/library?sort=no", c.token, "", 400},
		{"/api/library?filter=no", c.token, "", 400},
		{"/api/library?song=01", c.token, "", 400},
		{"/api/library?resource=invalid", c.token, "", 400},
		{"/api/library?q=" + strings.Repeat("x", 301), c.token, "", 400},
	} {
		r := httptest.NewRequest("GET", "http://"+c.address+tc.path, nil)
		r.Header.Set("X-SWD-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil || c.batch.Running || c.queue.Running {
		t.Fatal("viewing library started work")
	}
}
