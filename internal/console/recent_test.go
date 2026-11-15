package console

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
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
	for _, path := range []string{"/api/history", "/api/history/stats"} {
		for _, tc := range []struct {
			query, token, origin string
			status               int
		}{
			{"", "", "", 403}, {"", c.token, "http://evil.example", 403},
			{"?limit=501", c.token, "", 400}, {"?sort=invalid", c.token, "", 400}, {"?direction=invalid", c.token, "", 400}, {"?sort=song&direction=ascending", c.token, "", 400}, {"?from=oops", c.token, "", 400}, {"?from=10&to=9", c.token, "", 400}, {"?cursor=10:0", c.token, "", 400},
			{"", c.token, "", 200}, {"?from=1&to=999&limit=10&cursor=20:3", c.token, "", 200},
			{"?hideAdjacentCanceled=true", c.token, "", 200}, {"?hideAdjacentCanceled=false", c.token, "", 200}, {"?hideAdjacentCanceled=invalid", c.token, "", 400},
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

func TestHistoryReadsSurviveDatabaseWriteFailure(t *testing.T) {
	c := testConsole(t)
	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO request_events(requested_at,method,range_header,outcome,transferred_bytes,elapsed_ms,status,first_body_ns,first_body_blocking,download_bytes,cache_bytes,local_bytes)
VALUES (?,'GET','','completed',60,10,200,5000000,1,10,20,30)`, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	restore := failDatabaseWrites(t, c.settings.DatabasePath)
	if err := c.store.SetSongPolicy(t.Context(), 42, cacheproxy.SongFavorite); err == nil || c.store.Error() == nil {
		t.Fatal("write failure did not latch", err, c.store.Error())
	}
	for _, phase := range []string{"write-failure", "writes-restored"} {
		if phase == "writes-restored" {
			restore()
			if err := c.store.SetSongPolicy(t.Context(), 42, cacheproxy.SongFavorite); err != nil {
				t.Fatal("write conditions did not recover", err)
			}
		}
		t.Run(phase, func(t *testing.T) {
			for _, path := range []string{"/api/history", "/api/history/stats"} {
				t.Run(path, func(t *testing.T) {
					r := httptest.NewRequest("GET", "http://"+c.address+path, nil)
					r.Header.Set("X-SWD-Token", c.token)
					w := httptest.NewRecorder()
					c.ServeHTTP(w, r)
					if w.Code != 200 {
						t.Fatalf("readable history unavailable: %d %s", w.Code, w.Body.String())
					}
					var result struct {
						Requests json.RawMessage `json:"requests"`
						cacheproxy.RequestStatistics
					}
					if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					switch path {
					case "/api/history":
						var requests []cacheproxy.RecentRequest
						if err := json.Unmarshal(result.Requests, &requests); err != nil {
							t.Fatal(err)
						}
						if len(requests) != 1 || requests[0].Bytes != 60 {
							t.Fatal("lost committed request", w.Body.String())
						}
					case "/api/history/stats":
						if result.Blocking.Samples != 1 || result.Blocking.P50MS == nil || *result.Blocking.P50MS != 5 {
							t.Fatal("lost committed latency", w.Body.String())
						}
					}
				})
			}
			t.Run("events", func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t, afterFlush: cancel}
				c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/history/events", nil).WithContext(ctx))
				if w.Code != 200 || w.writes != 1 || !strings.Contains(w.Body.String(), "event: history\ndata: {\"closed\":false}") {
					t.Fatal("history subscription unavailable", w.Code, w.Body.String())
				}
			})
		})
	}
	if c.store.Error() == nil {
		t.Fatal("history reads cleared the retained storage fault")
	}
	if _, err := c.businessStore(); err == nil {
		t.Fatal("history reads bypassed business operation fault checks")
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("history read started engine or gateway")
	}
}

func TestHistoryRetiredReadAPIs(t *testing.T) {
	c := testConsole(t)
	for _, path := range []string{"/api/history/count", "/api/latency", "/api/response-sources"} {
		r := httptest.NewRequest("GET", "http://"+c.address+path, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("retired route %s returned %d", path, w.Code)
		}
	}
	for _, raw := range []string{"invalid", "2"} {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/history/stats?refresh="+raw, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid refresh %s returned %d", raw, w.Code)
		}
	}
}
