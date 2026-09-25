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
	c.recordActionError("/api/hosts/enable", errors.New("hosts failure"))
	post := func(body string) int {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(body))
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w.Code
	}
	if post(`{"storageDir":""}`) != 400 {
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
	if status.CDNError != "CDN failure" || status.ActionErrors["settings"] == "" || status.ActionErrors["hosts"] != "hosts failure" {
		t.Fatalf("errors mixed across sources: %+v", status)
	}
	body, _ := json.Marshal(c.settings)
	if post(string(body)) != 200 || c.actionErrors["settings"] != "" {
		t.Fatal("successful settings retry left an error")
	}
	if c.cdnError != "CDN failure" || c.actionErrors["hosts"] != "hosts failure" {
		t.Fatal("settings success cleared another source's error")
	}
	c.recordActionError("/api/hosts/enable", nil)
	if c.actionErrors["hosts"] != "" || c.cdnError != "CDN failure" {
		t.Fatal("hosts retry did not clear only its own error")
	}
	if err := c.stop(); err != nil || c.cdnError != "" {
		t.Fatal("successful stop left a CDN error")
	}
}
