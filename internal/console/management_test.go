package console

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementAdmissionThroughRouter(t *testing.T) {
	c := testConsole(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/library"}, {"GET", "/api/songs"}, {"POST", "/api/songs"},
		{"POST", "/api/cache/delete"}, {"POST", "/api/cache/open"},
		{"GET", "/api/downloads"}, {"GET", "/api/requests"},
		{"GET", "/api/history"}, {"GET", "/api/latency"}, {"GET", "/api/response-sources"},
		{"POST", "/api/library/preview"}, {"POST", "/api/batch/cancel"},
		{"POST", "/api/storage/create"}, {"POST", "/api/vrcx/preview"},
		{"POST", "/api/proxy/import"}, {"POST", "/api/settings"},
		{"POST", "/api/import/dismiss-suggestion"},
		{"POST", "/api/hosts/prepare"},
		{"POST", "/api/exit"}, {"POST", "/api/restart"},
		{"POST", "/api/terms/accept"}, {"POST", "/api/terms/leave"}, {"POST", "/api/terms/return"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, credentials := range []struct{ token, origin, host string }{
				{"", "", c.address},
				{"wrong", "http://" + c.address, c.address},
				{c.token, "http://evil.example", c.address},
				{c.token, "http://127.0.0.1:1", c.address},
				{c.token, "", "evil.example"},
			} {
				r := httptest.NewRequest(route.method, "http://"+credentials.host+route.path, strings.NewReader("{}"))
				r.Header.Set("X-SWD-Token", credentials.token)
				r.Header.Set("Origin", credentials.origin)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code != 403 {
					t.Fatalf("rejected credentials reached handler: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
	select {
	case <-c.ShutdownRequested():
		t.Fatal("rejected request initiated shutdown")
	default:
	}
	if c.service != nil || c.batch.Running || c.importStatus.Running {
		t.Fatal("rejected request started work")
	}
}

func TestManagementRoutingPreservesMethodsAndPublicReads(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		method, path string
		token        bool
		want         int
	}{
		{"GET", "/api/library", true, 200},
		{"GET", "/api/downloads", true, 200},
		{"GET", "/api/requests", true, 200},
		{"POST", "/api/library/preview", true, 409},
		{"POST", "/api/batch/cancel", true, 200},
		{"GET", "/api/import/dismiss-suggestion", true, 404},
		{"GET", "/api/hosts/prepare", true, 404},
		{"POST", "/api/library", true, 405},
		{"POST", "/api/library", false, 403},
		{"GET", "/api/cache/delete", true, 405},
		{"GET", "/api/cache/delete", false, 403},
		{"PUT", "/api/songs", true, 405},
		{"GET", "/api/terms/accept", false, 405},
		{"POST", "/api/downloads", true, 404},
		{"HEAD", "/api/downloads", false, 404},
		{"GET", "/api/unknown", false, 404},
		{"POST", "/api/unknown", false, 403},
		{"POST", "/api/unknown", true, 404},
		{"GET", "/api/identity", false, 200},
		{"GET", "/api/inventory", false, 200},
	} {
		for _, origin := range []string{"", "http://" + c.address} {
			r := httptest.NewRequest(tc.method, "http://"+c.address+tc.path, strings.NewReader("{}"))
			if tc.token {
				r.Header.Set("X-SWD-Token", c.token)
			}
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%+v origin %q: %d %s", tc, origin, w.Code, w.Body.String())
			}
		}
	}
	if c.service != nil {
		t.Fatal("read or rejected action started engine")
	}
}
