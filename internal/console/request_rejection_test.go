package console

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"still-wanna-dance/internal/faults"
)

func TestRequestRejectionsDoNotRaiseOrRecoverFaults(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	problem := errors.New("previous settings write failed")
	c.recordActionError("settings", problem)
	center.Take()
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/library?offset=invalid", ""},
		{"GET", "/api/songs?offset=-1", ""},
		{"GET", "/api/history?sort=invalid", ""},
		{"POST", "/api/cache/delete", "{"},
		{"POST", "/api/songs", `{"id":"invalid","policy":"invalid"}`},
		{"POST", "/api/settings", "{"},
		{"POST", "/api/settings", `{"downloadConcurrencySoftLimit":65}`},
		{"POST", "/api/settings/theme", `{"theme":"invalid"}`},
		{"POST", "/api/network/connectivity/start", `{"operation":"invalid"}`},
		{"POST", "/api/batch/download", `{}`},
		{"POST", "/api/path/select", `{"purpose":"invalid"}`},
		{"POST", "/api/import/start", `{"source":""}`},
		{"POST", "/api/storage/check", "{"},
		{"POST", "/api/vrcx/preview", "{"},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+c.address+tc.path, strings.NewReader(tc.body))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("rejection response changed: %d %s", w.Code, w.Body.String())
			}
			if open, cause := center.Take(); open {
				t.Fatal("request rejection opened the WebUI", cause)
			}
			center.Restore(problem)
			if open, _ := center.Take(); !open || c.actionErrors["settings"] != problem.Error() {
				t.Fatal("request rejection cleared the retained settings failure")
			}
		})
	}
	// A concurrent acceleration request is a normal admission rejection.
	c.activationMu.Lock()
	err := c.enableAcceleration(nil, nil)
	c.activationMu.Unlock()
	if err == nil {
		t.Fatal("busy acceleration was accepted")
	}
	c.recordActionError("activation", err)
	if open, cause := center.Take(); open {
		t.Fatal("busy acceleration opened the WebUI", cause)
	}
}

func TestRejectionDoesNotHideJoinedOperationalFailure(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	rejected := rejectRequest(errors.New("request rejected"))
	c.reportFault("test", errors.Join(rejected, context.Canceled))
	if open, cause := center.Take(); open {
		t.Fatal("rejection/cancellation opened the WebUI", cause)
	}
	problem := errors.New("rollback failed")
	c.reportFault("test", errors.Join(rejected, problem))
	if open, cause := center.Take(); !open || !errors.Is(cause, problem) {
		t.Fatal("joined operational failure was lost", cause)
	}
}
