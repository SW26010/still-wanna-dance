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

func TestTermsPageExitTimeout(t *testing.T) {
	for _, action := range []string{"leave", "reopen", "return", "accept", "accept-after-timeout"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			c, err := New(filepath.Join(root, "config.json"), "127.0.0.1:18081")
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
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
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
			r.Header.Set("X-StepStash-Token", tc.token)
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
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
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
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	request := func(method, path, body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-StepStash-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/start", "/api/activation/enable", "/api/hosts/enable", "/api/batch/start", "/api/batch/switch", "/api/settings", "/api/inventory/scan"} {
		if w := request("POST", path, "{}", c.token, ""); w.Code != 428 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	c.settings.AutoStartCDN = true
	c.AutoStart()
	if c.httpServer != nil || c.service != nil {
		t.Fatal("started before acceptance")
	}
	if err := c.start(); err == nil {
		t.Fatal("direct start bypass")
	}
	w := request("GET", "/", "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "软件许可不等于") || !strings.Contains(w.Body.String(), `id="accept" disabled`) || strings.Contains(w.Body.String(), "ZgotmplZ") {
		t.Fatal("missing usable consent page")
	}
	if w := request("POST", "/api/stop", "{}", c.token, ""); w.Code != 200 {
		t.Fatal("cleanup blocked")
	}
	if w := request("POST", "/api/hosts/disable", "{}", "", ""); w.Code != 403 {
		t.Fatal("cleanup bypasses authentication")
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
	if c.termsAccepted() {
		t.Fatal("invalid request accepted")
	}
	if w := request("POST", "/api/terms/accept", string(body), c.token, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reloaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if !reloaded.termsAccepted() {
		t.Fatal("receipt not persisted")
	}
	if w := request("GET", "/", "", "", ""); strings.Contains(w.Body.String(), `id="consent"`) {
		t.Fatal("still gated")
	}
	receipt, _ := json.Marshal(c.terms)
	for _, data := range []string{`broken`, `{"version":"old"}`, strings.Replace(string(receipt), legal.Hash(), "old-hash", 1)} {
		if err := os.WriteFile(c.configPath+".terms.json", []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		stale, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if stale.termsAccepted() {
			t.Fatal("stale or corrupt receipt accepted")
		}
		stale.Close()
	}
}

func TestTermsPersistenceFailureDoesNotAccept(t *testing.T) {
	root := t.TempDir()
	c, err := New(filepath.Join(root, "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := os.Mkdir(c.configPath+".terms.json", 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.acceptTerms(); err == nil {
		t.Fatal("expected write failure")
	}
	if c.termsAccepted() {
		t.Fatal("accepted without saved receipt")
	}
}
