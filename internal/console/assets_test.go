package console

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestPageLoadsEmbeddedAssets(t *testing.T) {
	c := testConsole(t)
	request := func(path, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	w := request("/", c.address)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	html := w.Body.String()
	if !strings.Contains(html, `name="stepstash-token" content="`+c.token+`"`) || strings.Contains(html, "__TOKEN__") {
		t.Fatal("page did not inject the action token")
	}
	refs := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(refs) != 4 {
		t.Fatalf("expected stylesheet and three scripts, got %v", refs)
	}
	for _, ref := range refs {
		path := ref[1]
		t.Run(path, func(t *testing.T) {
			w := request(path, c.address)
			if w.Code != http.StatusOK || w.Body.Len() == 0 {
				t.Fatal(w.Code, w.Body.String())
			}
			wantType := "text/css; charset=utf-8"
			if strings.HasSuffix(path, ".js") {
				wantType = "text/javascript; charset=utf-8"
			}
			if w.Header().Get("Content-Type") != wantType {
				t.Fatal(w.Header())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("asset lost response protections", w.Header())
			}
			if strings.Contains(w.Body.String(), c.token) {
				t.Fatal("static asset contains the session token")
			}
			if w := request(path, "external.example"); w.Code != http.StatusForbidden {
				t.Fatal("asset bypassed host validation", w.Code)
			}
		})
	}
	for _, path := range []string{"/assets/", "/assets/missing.js", "/assets/../index.html"} {
		if w := request(path, c.address); w.Code != http.StatusNotFound {
			t.Fatalf("unexpected asset path %s: %d", path, w.Code)
		}
	}
}
