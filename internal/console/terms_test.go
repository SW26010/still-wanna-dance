package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"still-wanna-dance/internal/legal"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Optional export lets the UI fixture preview the exact first-entry HTML from
// ServeHTTP, rather than an approximation of the accepted console.
func TestFirstEntryUsesModalConsent(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct{ path, name string }{{"/", "welcome.html"}, {"/terms", "terms.html"}} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+tc.path, nil))
		html := w.Body.String()
		if w.Code != 200 || !strings.Contains(html, `id="root"`) || !strings.Contains(html, `"termsGate":{"pageID":`) || !strings.Contains(html, `/assets/webui/app.js`) {
			t.Fatal("first-entry consent layout incomplete", tc.path)
		}
		if strings.Contains(html, "location.replace") || strings.Contains(html, `__TERMS_GATE__`) {
			t.Fatal("first entry still requires another document")
		}
		if dir := os.Getenv("SWD_UI_TERMS_EXPORT_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			// This is a test-only token. Replace it so the memory fixture can bind
			// its own token without exposing any live application's credentials.
			html = strings.ReplaceAll(html, c.token, "__TOKEN__")
			if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(html), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, path := range []string{"/api/status", "/api/inventory", "/api/downloads"} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+path, nil))
		if w.Code != 428 {
			t.Fatalf("business read %s bypassed consent: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/assets/webui/app.js", "/assets/webui/app.css", "/assets/logo.svg", "/favicon.ico", "/favicon-light.ico"} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+path, nil))
		if w.Code != 200 {
			t.Fatalf("consent asset %s unavailable: %d", path, w.Code)
		}
	}
	if _, err := os.Stat(c.files().ChannelKey); !os.IsNotExist(err) {
		t.Fatal("first entry created a business key")
	}
}

func TestTermsPageExitTimeout(t *testing.T) {
	for _, action := range []string{"leave", "reopen", "return", "accept", "accept-after-timeout"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			c, err := newConsoleWithWorkForTest(t, filepath.Join(root, "config.json"), "127.0.0.1:18081")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			synctest.Test(t, func(t *testing.T) {
				defer func() { c.mu.Lock(); c.cancelTermsExitLocked(); c.mu.Unlock() }()
				open := func() {
					c.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://"+c.address+"/terms", nil))
				}
				open()
				oldID := c.termsPageID
				c.termsPagePresence(oldID, true)
				time.Sleep(59 * time.Second)
				select {
				case <-c.ExitRequested():
					t.Fatal("exited before grace elapsed")
				default:
				}
				switch action {
				case "reopen":
					open()
					c.termsPagePresence(oldID, true) // delayed unload after refresh
				case "return":
					c.termsPagePresence(oldID, false)
				case "accept":
					if err := c.acceptTerms(); err != nil {
						t.Fatal(err)
					}
					c.termsPagePresence(oldID, true)
				}
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				wantExit := action == "leave" || action == "accept-after-timeout"
				select {
				case <-c.ExitRequested():
					if !wantExit {
						t.Fatal("cancelled timeout requested exit")
					}
				default:
					if wantExit {
						t.Fatal("timeout did not request exit")
					}
				}
				if action == "accept-after-timeout" && c.acceptTerms() == nil {
					t.Fatal("accepted after timeout committed shutdown")
				}
			})
		})
	}
}

func TestTermsPresenceAuthentication(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.termsPageID = "page"
	for _, path := range []string{"/api/terms/leave", "/api/terms/return"} {
		for _, tc := range []struct {
			method, host, token, origin, body string
			code                              int
		}{
			{"GET", c.address, c.token, "", `{"pageID":"page"}`, 405},
			{"POST", "evil.invalid", c.token, "", `{"pageID":"page"}`, 403},
			{"POST", c.address, "", "", `{"pageID":"page"}`, 403},
			{"POST", c.address, c.token, "https://evil.invalid", `{"pageID":"page"}`, 403},
			{"POST", c.address, c.token, "", `{}`, 400},
			{"POST", c.address, c.token, "", `{"pageID":"page"}`, 200},
		} {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+path, strings.NewReader(tc.body))
			r.Header.Set("X-SWD-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("%s: got %d want %d", path, w.Code, tc.code)
			}
			if tc.code != 200 && c.termsExitTimer != nil {
				t.Fatal("invalid request armed timer")
			}
		}
		c.mu.Lock()
		c.cancelTermsExitLocked()
		c.mu.Unlock()
	}
}

func TestCloseCancelsTermsTimer(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.termsPageID = "page"
	c.termsPagePresence("page", true)
	if c.termsExitTimer == nil {
		t.Fatal("missing timer")
	}
	c.Close()
	if c.termsExitTimer != nil {
		t.Fatal("timer survived close")
	}
	c.termsPagePresence("page", true)
	if c.termsExitTimer != nil {
		t.Fatal("timer armed after close")
	}
}

func TestTermsGateAndPersistence(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	started := stubNetworkDiagnostics(t, c)
	request := func(method, path, body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-SWD-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/start", "/api/activation/enable", "/api/hosts/enable", "/api/batch/start", "/api/settings", "/api/inventory/scan"} {
		if w := request("POST", path, "{}", c.token, ""); w.Code != 428 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if c.settings.AutoAccelerate || !c.settings.QueueWithGateway {
		t.Fatal("automatic acceleration should default off and queue following should default on")
	}
	c.AutoStart()
	if c.httpServer != nil || c.service != nil || c.queue.Running || c.queueDesired {
		t.Fatal("started before acceptance")
	}
	if err := c.start(); err == nil {
		t.Fatal("direct start bypass")
	}
	w := request("GET", "/", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `/assets/webui/app.js`) || !strings.Contains(w.Body.String(), `"termsGate":{"pageID":`) || strings.Contains(w.Body.String(), "ZgotmplZ") {
		t.Fatal("missing usable consent page")
	}
	if w := request("POST", "/api/stop", "{}", c.token, ""); w.Code != 200 {
		t.Fatal("cleanup blocked")
	}
	if w := request("POST", "/api/hosts/disable", "{}", "", ""); w.Code != 403 {
		t.Fatal("cleanup bypasses authentication")
	}
	w = request("GET", "/api/terms/current", "", "", "")
	var current map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil || w.Code != 200 || current["text"] != legal.Text || current["sha256"] != legal.Hash() || current["version"] != legal.Version || current["accepted"] != false {
		t.Fatal("current terms unavailable before acceptance", err)
	}
	if c.networkDiagnostics.connectivity.Running {
		t.Fatal("viewing terms started diagnosis")
	}
	body, _ := json.Marshal(map[string]any{"version": legal.Version, "sha256": legal.Hash(), "agree": true, "contentRights": true})
	for _, tc := range []struct {
		body, token, origin string
		code                int
	}{
		{string(body), "", "", 403}, {string(body), c.token, "https://evil.invalid", 403},
		{`{}`, c.token, "", 400}, {strings.Replace(string(body), legal.Version, "old", 1), c.token, "", 400},
		{strings.Replace(string(body), `"contentRights":true`, `"contentRights":false`, 1), c.token, "", 400},
		{strings.Replace(string(body), legal.Hash(), "wrong-hash", 1), c.token, "", 400},
	} {
		if w := request("POST", "/api/terms/accept", tc.body, tc.token, tc.origin); w.Code != tc.code {
			t.Fatalf("bad acceptance %d", w.Code)
		}
	}
	if c.termsAccepted() || c.networkDiagnostics.connectivity.Running {
		t.Fatal("invalid request accepted or started diagnosis")
	}
	if w := request("POST", "/api/terms/accept", string(body), c.token, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-started:
		t.Fatal("consent must not start manual diagnostics")
	default:
	}
	if c.service != nil || c.queueCancel != nil {
		t.Fatal("consent started business services")
	}
	reloaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if !reloaded.termsAccepted() {
		t.Fatal("receipt not persisted")
	}
	assertCurrentTermsAccepted(t, reloaded, true)
	if w := request("GET", "/", "", "", ""); !strings.Contains(w.Body.String(), `"termsGate":null`) {
		t.Fatal("still gated")
	}
	receipt, _ := json.Marshal(c.terms)
	for _, data := range []string{`broken`, `{"version":"old"}`, strings.Replace(string(receipt), legal.Hash(), "old-hash", 1)} {
		if err := os.WriteFile(c.files().Terms, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		stale, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if stale.termsAccepted() {
			t.Fatal("stale or corrupt receipt accepted")
		}
		assertCurrentTermsAccepted(t, stale, false)
		stale.Close()
	}
}

func assertCurrentTermsAccepted(t *testing.T, c *Console, want bool) {
	t.Helper()
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/terms/current", nil))
	var current map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil || w.Code != 200 || current["accepted"] != want {
		t.Fatalf("current terms acceptance: want %v, got %s (%v)", want, w.Body, err)
	}
	if c.networkDiagnostics != nil {
		t.Fatal("reading current consent started diagnosis")
	}
}

func TestTermsPersistenceFailureDoesNotAccept(t *testing.T) {
	root := t.TempDir()
	c, err := newConsoleWithWorkForTest(t, filepath.Join(root, "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := os.MkdirAll(c.files().Terms, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.acceptTerms(); err == nil {
		t.Fatal("expected write failure")
	}
	if c.termsAccepted() {
		t.Fatal("accepted without saved receipt")
	}
	body, _ := json.Marshal(map[string]any{"version": legal.Version, "sha256": legal.Hash(), "agree": true, "contentRights": true})
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/terms/accept", strings.NewReader(string(body)))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 500 || c.termsAccepted() || c.networkDiagnostics != nil {
		t.Fatal("failed receipt write granted consent or started diagnosis")
	}
}

func TestTermsReconfirmationDistinguishesMissingAndUnverifiableRecords(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	check := func(accepted, needsConfirmation bool) {
		t.Helper()
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/terms/current", nil))
		var current struct {
			Accepted          bool `json:"accepted"`
			NeedsConfirmation bool `json:"needsConfirmation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil || w.Code != 200 || current.Accepted != accepted || current.NeedsConfirmation != needsConfirmation {
			t.Fatalf("unexpected confirmation state: %s (%v)", w.Body, err)
		}
	}
	check(false, false)
	if err := os.MkdirAll(c.files().StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`broken`, `{"format":1,"protection":"dpapi-current-user","ciphertext":"AA=="}`} {
		if err := os.WriteFile(c.files().Terms, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := c.loadTerms(); err != nil {
			t.Fatal(err)
		}
		check(false, true)
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	if err := c.loadTerms(); err != nil {
		t.Fatal(err)
	}
	check(true, false)
}
