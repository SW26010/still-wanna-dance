package console

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionErrorsAreScopedAndClearOnRetry(t *testing.T) {
	c := testConsole(t)
	c.cdnError = "CDN failure"
	c.recordActionError("/api/activation/enable", errors.New("activation failure"))
	post := func(body string) int {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w.Code
	}
	if post(`{"videoStorageMode":"invalid"}`) != 400 {
		t.Fatal("expected invalid settings failure")
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status struct {
		CDNError     string            `json:"cdnError"`
		ActionErrors map[string]string `json:"actionErrors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.CDNError != "CDN failure" || status.ActionErrors["settings"] == "" || status.ActionErrors["activation"] != "activation failure" {
		t.Fatalf("errors mixed across sources: %+v", status)
	}
	body, _ := json.Marshal(c.settings)
	if post(string(body)) != 200 || c.actionErrors["settings"] != "" {
		t.Fatal("successful settings retry left an error")
	}
	if c.cdnError != "CDN failure" || c.actionErrors["activation"] != "activation failure" {
		t.Fatal("settings success cleared another source's error")
	}
	c.recordActionError("/api/activation/enable", nil)
	if c.actionErrors["activation"] != "" || c.cdnError != "CDN failure" {
		t.Fatal("activation retry did not clear only its own error")
	}
	if err := c.stop(); err != nil || c.cdnError != "" {
		t.Fatal("successful stop left a CDN error")
	}
}
