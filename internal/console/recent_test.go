package console

import (
	"bytes"
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
		r.Header.Set("X-SWD-Token", tc.token)
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
	if err := os.MkdirAll(testStorageRoot(c.settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testStorageRoot(c.settings), "storage.sqlite"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/requests", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 500 || strings.Contains(w.Body.String(), testStorageRoot(c.settings)) {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
}

func TestHistoryAndLatencyAPIAuthRangeAndOffline(t *testing.T) {
	c := testConsole(t)
	path := libraryPaths(c.settings).DatabasePath
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/history", "/api/latency", "/api/response-sources"} {
		for _, tc := range []struct {
			query, token, origin string
			status               int
		}{
			{"", "", "", 403}, {"", c.token, "http://evil.example", 403},
			{"?limit=501", c.token, "", 400}, {"?sort=invalid", c.token, "", 400}, {"?direction=invalid", c.token, "", 400}, {"?sort=song&direction=ascending", c.token, "", 200}, {"?from=oops", c.token, "", 400}, {"?from=10&to=9", c.token, "", 400}, {"?cursor=10:0", c.token, "", 400},
			{"", c.token, "", 200}, {"?from=1&to=999&limit=10&cursor=20:3", c.token, "", 200},
		} {
			r := httptest.NewRequest("GET", "http://"+c.address+path+tc.query, nil)
			r.Header.Set("X-SWD-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%s %+v: %d %s", path, tc, w.Code, w.Body.String())
			}
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("history read started engine")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatal("history read changed the database", err)
	}
}
