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
	if !strings.Contains(html, `name="swd-token" content="`+c.token+`"`) || strings.Contains(html, "__TOKEN__") {
		t.Fatal("page did not inject the action token")
	}
	scripts := regexp.MustCompile(`<script src="(/assets/[^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 5 || !strings.Contains(html, `rel="stylesheet" href="/assets/console.css"`) {
		t.Fatalf("expected stylesheet and five scripts, got %v", scripts)
	}
	assetRefs := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+|/favicon\.ico)"`)
	var refs [][]string
	for _, pagePath := range []string{"/", "/help", "/about", "/terms"} {
		page := request(pagePath, c.address)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `rel="icon" href="/assets/logo.svg"`) || !strings.Contains(page.Body.String(), `class="brand-logo"`) {
			t.Fatalf("page %s lacks application branding: %d", pagePath, page.Code)
		}
		refs = append(refs, assetRefs.FindAllStringSubmatch(page.Body.String(), -1)...)
	}
	// Capacity research belongs in Help, outside the short setup flow.
	figures := regexp.MustCompile(`<a href="(/assets/[^"]+\.svg)"`).FindAllStringSubmatch(request("/help", c.address).Body.String(), -1)
	if len(figures) != 1 {
		t.Fatalf("expected capacity reference in Help, got %v", figures)
	}
	seen := make(map[string]bool)
	for _, ref := range refs {
		path := ref[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		t.Run(path, func(t *testing.T) {
			w := request(path, c.address)
			if w.Code != http.StatusOK || w.Body.Len() == 0 {
				t.Fatal(w.Code, w.Body.String())
			}
			wantType := "text/css; charset=utf-8"
			if strings.HasSuffix(path, ".js") {
				wantType = "text/javascript; charset=utf-8"
			}
			if strings.HasSuffix(path, ".svg") {
				wantType = "image/svg+xml"
			}
			if strings.HasSuffix(path, ".ico") {
				wantType = "image/x-icon"
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
