package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestLibraryAPIReadOnlyAndValidation(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	for _, tc := range []struct {
		path, token, origin string
		want                int
	}{
		{"/api/library", c.token, "", 200},
		{"/api/library?covers=1", c.token, "", 200},
		{"/api/library?covers=1", "", "", 403},
		{"/api/library?covers=invalid", c.token, "", 400},
		{"/api/library?view=resources&covers=1", c.token, "", 400},
		{"/api/library?view=resources&sort=recent", c.token, "", 200},
		{"/api/library", "", "", 403},
		{"/api/library", c.token, "http://evil.example", 403},
		{"/api/library?view=no", c.token, "", 400},
		{"/api/library?offset=-1", c.token, "", 400},
		{"/api/library?sort=no", c.token, "", 400},
		{"/api/library?sort=title&direction=descending", c.token, "", 200},
		{"/api/library?view=resources&sort=resource&direction=ascending", c.token, "", 200},
		{"/api/library?direction=invalid", c.token, "", 400},
		{"/api/library?filter=no", c.token, "", 400},
		{"/api/library?song=01", c.token, "", 400},
		{"/api/library?resource=invalid", c.token, "", 400},
		{"/api/library?q=id:1", c.token, "", 200},
		{"/api/library?view=resources&q=ID:1", c.token, "", 200},
		{"/api/library?q=md5:" + strings.Repeat("A", 32), c.token, "", 200},
		{"/api/library?q=id:0", c.token, "", 400},
		{"/api/library?q=md5:abc", c.token, "", 400},
		{"/api/library?song=1&q=test", c.token, "", 400},
		{"/api/library?song=1&resource=" + strings.Repeat("a", 32), c.token, "", 400},
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
	if c.service != nil || c.httpServer != nil || c.batch.Running || c.queue.snapshot().Running {
		t.Fatal("viewing library started work")
	}
}

func TestLibraryPageChoosesCoversLocally(t *testing.T) {
	c := testConsole(t)
	createReaderLibrary(t, c)
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261113000000", Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: strings.Repeat("a", 32)}, {ID: 2, MD5: strings.Repeat("a", 32)}, {ID: 3, MD5: strings.Repeat("a", 32)}}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.files().Root, "data", "covers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jpg"), coverJPEG(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.jpg"), []byte("not a JPEG"), 0600); err != nil {
		t.Fatal(err)
	}
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("page inspection fetched upstream")
		return nil, nil
	})
	for _, query := range []string{"", "?covers=1"} {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/library"+query, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var page libraryPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if query == "" {
			if page.Covers != nil || len(c.covers.validated) != 0 {
				t.Fatal("list view inspected covers", page.Covers)
			}
			continue
		}
		if len(page.Covers) != len(page.Songs) || page.Covers["1"] != (songCoverChoice{Source: "local", Refresh: true}) || page.Covers["2"].Source != "category" || page.Covers["3"].Source != "category" {
			t.Fatal("non-deterministic cover choices", page.Covers)
		}
	}
}
