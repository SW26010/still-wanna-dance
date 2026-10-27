package console

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCacheDeletionUsesApplicationStoreWithoutEngine(t *testing.T) {
	c := testConsole(t)
	paths := libraryPaths(c.settings)
	key := strings.Repeat("a", 32)
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261026000000", Source: "test", Songs: []cacheproxy.CatalogSong{{ID: 42, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.VideoDir, key+".mp4")
	if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := cacheproxy.ReadLibraryPage(t.Context(), paths, cacheproxy.LibraryQuery{View: "resources", Filter: "saved", Search: "", Sort: "", Offset: 0})
	if err != nil || len(page.Keys) != 1 {
		t.Fatal("cannot select fixture video", page, err)
	}
	body, _ := json.Marshal(map[string]any{"storageID": page.StorageID, "entries": []cacheproxy.CacheSelection{{Key: key, Stamp: (*page.Resources[page.Keys[0]].Local).Stamp}}})
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/cache/delete", strings.NewReader(string(body)))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "deleted") {
		t.Fatal("deletion could not use the application's owned store", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("selected video remained", err)
	}
	if c.service != nil {
		t.Fatal("manual deletion started the video engine")
	}
	if _, err := c.store.SongUsageCount(t.Context()); err != nil {
		t.Fatal("manual deletion closed the application database", err)
	}
}

func TestLibrarySearchLengthMatchesHTMLMaxlength(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	for _, tc := range []struct {
		name, query string
		status      int
	}{
		{"ASCII boundary", strings.Repeat("a", 300), 200},
		{"ASCII too long", strings.Repeat("a", 301), 400},
		{"Chinese regression", strings.Repeat("舞", 101), 200},
		{"Chinese boundary", strings.Repeat("舞", 300), 200},
		{"Chinese too long", strings.Repeat("舞", 301), 400},
		{"supplementary boundary", strings.Repeat("😀", 150), 200},
		{"supplementary too long", strings.Repeat("😀", 151), 400},
		{"mixed boundary", strings.Repeat("舞", 298) + "😀", 200},
		{"mixed too long", strings.Repeat("舞", 299) + "😀", 400},
		{"invalid UTF-8", "\xff", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+c.address+"/api/library?q="+url.QueryEscape(tc.query), nil)
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want=%d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestCacheAPIAuthAndOfflineRead(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	for _, path := range []string{"/api/library", "/api/cache/delete", "/api/cache/open"} {
		method := "POST"
		if path == "/api/library" {
			method = "GET"
		}
		for _, tc := range []struct{ host, token, origin string }{{c.address, "", ""}, {"evil.example", c.token, ""}, {c.address, c.token, "http://evil.example"}} {
			r := httptest.NewRequest(method, "http://"+tc.host+path, strings.NewReader(`{}`))
			r.Header.Set("X-SWD-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(path, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"/api/library", 200}, {"/api/library?offset=-1", 400}, {"/api/library?sort=invalid", 400}} {
		r := httptest.NewRequest("GET", "http://"+c.address+tc.path, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("read started service")
	}
}

func TestLibraryGETDoesNotAcquireLifecycleLock(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/library", nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		done <- w
	}()
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("library GET waited for lifecycle lock")
	}
}

func TestCacheAPIScanAndStaleStorageProtection(t *testing.T) {
	c := testConsole(t)
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	b, _ := json.Marshal(map[string]any{"storageID": cacheproxy.CacheStorageID(libraryPaths(c.settings)), "entries": []cacheproxy.CacheSelection{{Key: strings.Repeat("a", 32), Stamp: "old"}}})
	c.batch = Batch{Running: true, ScanOnly: true}
	w := post("/api/cache/delete", string(b))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "扫描校验") {
		t.Fatal(w.Code, w.Body.String())
	}
	c.batch = Batch{}
	for _, path := range []string{"/api/cache/open", "/api/cache/delete"} {
		w = post(path, `{"storageID":"stale","entries":[]}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "存储目录已切换") {
			t.Fatal(w.Code, w.Body.String())
		}
		w = post(path, `{"path":"C:/Windows"}`)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("rejected action started engine")
	}
}
