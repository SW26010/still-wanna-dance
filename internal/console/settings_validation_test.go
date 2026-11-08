package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/workfiles"
)

func TestSettingsValidationRetainsWriteFailureUntilSaved(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	before := c.editor.savedSettings
	config, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	// Occupy the reserved atomic-write slot to fail the real persistence path.
	blocked := filepath.Join(filepath.Dir(c.configPath), workfiles.TempName(c.configPath))
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/settings", strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"logLevel":"debug"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("write unexpectedly succeeded: %d %s", w.Code, w.Body.String())
	}
	failure := c.actionErrors["settings"]
	open, cause := center.Take()
	if failure == "" || !open || cause == nil {
		t.Fatal("write failure was not retained in both boundaries")
	}
	if w := post(`{"downloadConcurrencySoftLimit":65}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"settings_validation"`) {
		t.Fatalf("expected field rejection: %d %s", w.Code, w.Body.String())
	}
	if c.actionErrors["settings"] != failure {
		t.Error("field rejection cleared or replaced the previous write failure")
	}
	if open, _ := center.Take(); open {
		t.Error("field rejection opened another incident")
	}
	center.Restore(cause)
	if open, _ := center.Take(); !open {
		t.Error("field rejection cleared the unresolved incident")
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || !bytes.Equal(config, after) || c.editor.savedSettings != before {
		t.Fatal("failed attempts changed settings", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if w := post(`{"logLevel":"debug"}`); w.Code != http.StatusOK {
		t.Fatalf("retry failed: %d %s", w.Code, w.Body.String())
	}
	if c.actionErrors["settings"] != "" || c.editor.savedSettings.LogLevel != "debug" {
		t.Error("successful save did not clear the old operation failure")
	}
	center.Restore(cause)
	if open, _ := center.Take(); open {
		t.Error("successful save left the old fault incident active")
	}
}

func TestSettingsValidationDoesNotOpenWebUIOrWriteConfig(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ body, field string }{
		{`{"upstreamMode":"proxy","proxyBackend":"mihomo"}`, "proxyImportText"},
		{`{"upstreamMode":"proxy","proxyBackend":"external-socks5","socks5Address":"%%%:7891"}`, "socks5Address"},
		{`{"downloadConcurrencySoftLimit":65}`, "downloadConcurrencySoftLimit"},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/settings", strings.NewReader(tc.body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var result struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || result.Code != "settings_validation" || result.Fields[tc.field] == "" {
			t.Errorf("missing field validation: %d %s", w.Code, w.Body.String())
		}
		if open, cause := center.Take(); open {
			t.Errorf("validation opened WebUI: %v", cause)
		}
		after, err := os.ReadFile(c.configPath)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("validation changed config", err)
		}
	}
}
