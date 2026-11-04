package console

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"still-wanna-dance/internal/faults"
)

func TestUnrelatedNetworkSuccessKeepsOperationFault(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	problem := errors.New("calibration could not start")
	c.recordActionError("network-calibration", problem)
	center.Take()
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/network/connectivity/cancel", strings.NewReader(`{}`))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	center.Restore(problem)
	if open, _ := center.Take(); !open || c.actionErrors["network-calibration"] != problem.Error() {
		t.Fatal("connectivity cancellation cleared a calibration fault")
	}
	c.recordActionError("network-calibration", nil)
	center.Restore(problem)
	if open, _ := center.Take(); open || c.actionErrors["network-calibration"] != "" {
		t.Fatal("successful calibration retry did not clear its own fault")
	}
}

func TestActionErrorsAreScopedAndClearOnRetry(t *testing.T) {
	c := testConsole(t)
	c.cdnError = "CDN failure"
	c.recordActionError("activation", errors.New("activation failure"))
	post := func(body string) int {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w.Code
	}
	// Field validation is returned to its form, without a retained global error.
	if post(`{"videoStorageMode":"invalid"}`) != 400 || c.actionErrors["settings"] != "" {
		t.Fatal("field validation was retained as an action failure")
	}
	// An operational failure still owns the settings action until a successful retry.
	c.recordActionError("settings", errors.New("configuration write failure"))
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
	c.recordActionError("activation", nil)
	if c.actionErrors["activation"] != "" || c.cdnError != "CDN failure" {
		t.Fatal("activation retry did not clear only its own error")
	}
	if err := c.stop(); err != nil || c.cdnError != "" {
		t.Fatal("successful stop left a CDN error")
	}
}
