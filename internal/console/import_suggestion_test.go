package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func firstRunImportConsole(t *testing.T) *Console {
	t.Helper()
	c, err := newConsole(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.hosts, _ = fixtureHostsController(t, "127.0.0.1 api.udon.dance # user\r\n")
	c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	return c
}

func videoImportSuggested(t *testing.T, c *Console) bool {
	t.Helper()
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var result struct {
		Suggested bool `json:"videoImportSuggested"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return result.Suggested
}

func TestVideoImportSuggestionRequiresUnmanagedLocalAPI(t *testing.T) {
	c := firstRunImportConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, hosts string
		want        bool
	}{
		{"missing", hostsBaseline, false},
		{"user", "127.0.0.1 api.udon.dance # user\r\n", true},
		{"other tool", "127.0.0.1 API.UDON.DANCE. # Another tool\r\n", true},
		{"managed", "\r\n127.0.0.1 api.udon.dance # SWD managed\r\n", false},
		{"other domain", "127.0.0.1 play.udon.dance\r\n", false},
		{"comment", "# 127.0.0.1 api.udon.dance\r\n", false},
		{"conflict", "192.0.2.1 api.udon.dance\r\n", false},
		{"IPv6", "::1 api.udon.dance\r\n", false},
		{"mixed conflict", "127.0.0.1 api.udon.dance\r\n192.0.2.1 api.udon.dance\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, launches := fixtureHostsController(t, tc.hosts)
			c.hosts = h
			if got := videoImportSuggested(t, c); got != tc.want {
				t.Fatalf("suggested = %v, want %v", got, tc.want)
			}
			if *launches != 0 || readHostsFixture(t, h.path) != tc.hosts || c.service != nil || c.importStatus.Running {
				t.Fatal("suggestion changed hosts or started work")
			}
		})
	}
	c.hosts, _ = fixtureHostsController(t, "")
	if err := os.Remove(c.hosts.path); err != nil {
		t.Fatal(err)
	}
	if videoImportSuggested(t, c) {
		t.Fatal("unreadable hosts suggested an import")
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("status changed configuration", err)
	}
}

func TestVideoImportSuggestionFirstRunAndAcknowledgement(t *testing.T) {
	for _, dismiss := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhandled", true: "dismissed"}[dismiss], func(t *testing.T) {
			c := firstRunImportConsole(t)
			if !videoImportSuggested(t, c) {
				t.Fatal("first run lost suggestion")
			}
			before, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if dismiss {
				for range 2 {
					r := httptest.NewRequest("POST", "http://"+c.address+"/api/import/dismiss-suggestion", strings.NewReader("{}"))
					r.Header.Set("X-SWD-Token", c.token)
					w := httptest.NewRecorder()
					c.ServeHTTP(w, r)
					if w.Code != 200 || videoImportSuggested(t, c) {
						t.Fatal("acknowledgement failed", w.Code, w.Body.String())
					}
				}
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("acknowledgement changed configuration", err)
			}
			hosts := c.hosts
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newConsole(c.configPath, c.address, newFixtureDNS(), testWorkDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			reopened.hosts = hosts
			reopened.videoAddress, reopened.httpsAddress = "127.0.0.1:0", "127.0.0.1:0"
			if videoImportSuggested(t, reopened) {
				t.Fatal("reopening an existing configuration repeated the first-run suggestion")
			}
		})
	}
}
