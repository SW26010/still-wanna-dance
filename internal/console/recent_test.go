package console

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecentAPIAuthLimitsAndOffline(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		host, token, origin, query string
		status                     int
	}{
		{c.address, "", "", "", 403},
		{"evil.example", c.token, "", "", 403},
		{c.address, c.token, "http://evil.example", "", 403},
		{c.address, c.token, "", "?limit=501", 400},
		{c.address, c.token, "", "?limit=-1", 400},
		{c.address, c.token, "", "?limit=no", 400},
		{c.address, c.token, "", "", 200},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/requests"+tc.query, nil)
		r.Header.Set("X-StepStash-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("read started engine")
	}
	if err := os.MkdirAll(c.settings.StorageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.settings.StorageDir, "stepstash.sqlite"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/requests", nil)
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), c.settings.StorageDir) {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
}
