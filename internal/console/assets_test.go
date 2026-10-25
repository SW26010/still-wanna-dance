package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/legal"
)

func TestWebUIDocumentNavigationBootstrap(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "before-consent"}[accepted], func(t *testing.T) {
			c := testConsole(t)
			if !accepted {
				c.terms = termsReceipt{}
			}
			c.settings.StorageDir = `<script>alert("path")</script>`
			c.savedSettings.StorageDir = "pending-storage"
			c.settings.Theme = "dark"
			for _, path := range []string{"/", "/help", "/about", "/terms"} {
				w := httptest.NewRecorder()
				c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+path, nil))
				match := regexp.MustCompile(`<script id="swd-bootstrap" type="application/json">(.*?)</script>`).FindStringSubmatch(w.Body.String())
				if w.Code != http.StatusOK || len(match) != 2 {
					t.Fatalf("%s: missing bootstrap, status %d", path, w.Code)
				}
				var data struct {
					Page, Token, Theme string
					Paths              map[string]string
					Build              json.RawMessage
					Terms              struct{ Text, Version string }
					TermsGate          *termsEntry
				}
				if err := json.Unmarshal([]byte(match[1]), &data); err != nil {
					t.Fatal(err)
				}
				wantPage := "console"
				if !accepted && (path == "/help" || path == "/about") {
					wantPage = path[1:]
				}
				if data.Page != wantPage || data.Token != c.token {
					t.Fatalf("%s: wrong page or token", path)
				}
				if data.Theme != "dark" {
					t.Fatalf("%s: saved theme missing at page startup", path)
				}
				if data.Page == "console" || data.Page == "help" {
					if data.Paths["Storage"] != c.settings.StorageDir || data.Paths["Config"] != c.files().Config || strings.Contains(w.Body.String(), c.settings.StorageDir) {
						t.Fatalf("%s: missing, stale or unescaped paths", path)
					}
				}
				if data.Page == "console" {
					if len(data.Build) == 0 || data.Terms.Text != legal.Text || data.Terms.Version != legal.Version {
						t.Fatalf("%s: document content unavailable at app startup", path)
					}
				}
				wantGate := !accepted && (path == "/" || path == "/terms")
				if (data.TermsGate != nil) != wantGate {
					t.Fatalf("%s: consent gate changed", path)
				}
			}
			if c.termsAccepted() != accepted {
				t.Fatal("reading documents changed consent")
			}
		})
	}
}

func TestLicenseAssetsAreAvailableOfflineBeforeConsent(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	for _, name := range []string{"software-license.txt", "third-party-notices.txt", "webui-notices.txt"} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/assets/webui/"+name, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Body.Len() < 1000 {
			t.Fatal(name, w.Code, w.Header(), w.Body.Len())
		}
	}
	if c.service != nil || c.termsAccepted() {
		t.Fatal("license read started engine or accepted terms")
	}
}

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
	scripts := regexp.MustCompile(`<script type="module" src="(/assets/[^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 1 || !strings.Contains(html, `rel="stylesheet" href="/assets/webui/app.css"`) {
		t.Fatalf("expected Fluent stylesheet and module, got %v", scripts)
	}
	assetRefs := regexp.MustCompile(`(?:href|src)="(/assets/[^"]+|/favicon\.ico)"`)
	var refs [][]string
	for _, pagePath := range []string{"/", "/help", "/about", "/terms"} {
		page := request(pagePath, c.address)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `rel="icon" href="/favicon.ico"`) {
			t.Fatalf("page %s lacks application branding: %d", pagePath, page.Code)
		}
		refs = append(refs, assetRefs.FindAllStringSubmatch(page.Body.String(), -1)...)
	}
	refs = append(refs, []string{"", "/assets/storage-benefit.svg"}, []string{"", "/assets/logo.svg"}, []string{"", "/favicon-light.ico"})
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
			if path == "/favicon.ico" && !bytes.Equal(w.Body.Bytes(), branding.ICO) ||
				path == "/favicon-light.ico" && !bytes.Equal(w.Body.Bytes(), branding.LightICO) {
				t.Fatal("favicon does not match its theme")
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
