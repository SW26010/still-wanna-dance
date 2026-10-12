package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupLogInspection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if got := inspectSetupLogDirectory(dir); got.State != "unknown" || !strings.Contains(got.Message, "目录") {
		t.Fatal(got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("inspection created directory")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Unrelated files and directories matching the pattern are not logs.
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "output_log_9999.txt"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := inspectSetupLogDirectory(dir); got.State != "unknown" || got.Path != "" {
		t.Fatal(got)
	}
	old := filepath.Join(dir, "output_log_2020-01-01.txt")
	latest := filepath.Join(dir, "output_log_2020-01-02.txt")
	for _, path := range []string{old, latest} {
		if err := os.WriteFile(path, []byte("private content\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A newer mtime on an old session must not win over the session filename.
	if err := os.Chtimes(latest, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	got := inspectSetupLogDirectory(dir)
	if got.State != "ok" || got.Path != latest || strings.Contains(got.Message, "运行") || strings.Contains(got.Message, "private") {
		t.Fatal(got)
	}
	if err := os.WriteFile(latest, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := inspectSetupLogDirectory(dir); got.State != "ok" {
		t.Fatal("empty log is readable", got)
	}
	if got := inspectSetupLogDirectory(old); got.State != "failed" {
		t.Fatal("file cannot be inspected as directory", got)
	}
}

func TestSetupLogAPIGatesAndActiveLocation(t *testing.T) {
	c := testConsole(t)
	c.settings.LogDir = t.TempDir()
	c.savedSettings.LogDir = filepath.Join(t.TempDir(), "pending")
	path := filepath.Join(c.settings.LogDir, "output_log_2020-01-01.txt")
	if err := os.WriteFile(path, []byte("sensitive log text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := c.settings
	accepted := c.terms
	for _, tc := range []struct {
		accepted      bool
		token, origin string
		code          int
	}{
		{false, c.token, "", 428}, {true, "", "", 403}, {true, c.token, "https://example.invalid", 403}, {true, c.token, "", 200},
	} {
		c.terms = termsReceipt{}
		if tc.accepted {
			c.terms = accepted
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/logs", strings.NewReader("{}"))
		r.Header.Set("X-SWD-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("got %d, want %d: %s", w.Code, tc.code, w.Body)
		}
		if tc.code == 200 {
			var got setupLogs
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.State != "ok" || got.Path != path || strings.Contains(w.Body.String(), "sensitive") {
				t.Fatal(got, err)
			}
		}
	}
	if c.settings != before || c.queueCancel != nil || c.service != nil {
		t.Fatal("inspection changed settings or started service")
	}
	if _, err := os.Stat(c.savedSettings.LogDir); !os.IsNotExist(err) {
		t.Fatal("inspection touched pending location")
	}
}
