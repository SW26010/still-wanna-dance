package console

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupProgressCannotAuthorizeTerms(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := SetupProgress{SchemaVersion: 1, Step: "finish", State: "completed"}
	if err := c.saveSetupProgress(p); err == nil {
		t.Fatal("saved progress without consent")
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "http://"+c.address+"/api/setup", strings.NewReader(`{"schemaVersion":1,"step":"finish","state":"completed"}`))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 428 {
			t.Fatalf("%s setup gate: %d", method, w.Code)
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.files().State("setup")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.files().State("setup"), []byte(`{"schemaVersion":1,"step":"finish","state":"completed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c.termsAccepted() {
		t.Fatal("progress authorized terms")
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/", nil))
	if !strings.Contains(w.Body.String(), `id="welcome"`) || !strings.Contains(w.Body.String(), `id="termsPanel" hidden`) {
		t.Fatal("missing welcome before consent")
	}
}

func TestSetupProgressPersistenceAndValidation(t *testing.T) {
	c := testConsole(t)
	p, err := c.setupProgress()
	if err != nil || p.Step != "welcome" {
		t.Fatalf("initial progress: %+v %v", p, err)
	}
	for _, state := range []string{"active", "completed", "skipped"} {
		want := SetupProgress{SchemaVersion: 1, Step: "storage", State: state, FurthestStep: "proxy"}
		if err := c.saveSetupProgress(want); err != nil {
			t.Fatal(err)
		}
		got, err := c.setupProgress()
		if err != nil || got != want {
			t.Fatalf("round trip: %+v %v", got, err)
		}
	}
	before, err := os.ReadFile(c.files().State("setup"))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []SetupProgress{
		{SchemaVersion: 2, Step: "storage", State: "active"},
		{SchemaVersion: 1, Step: "unknown", State: "completed"},
		{SchemaVersion: 1, Step: "finish", State: "accepted"},
		{SchemaVersion: 1, Step: "storage", State: "active", FurthestStep: "unknown"},
	} {
		if err := c.saveSetupProgress(invalid); err == nil {
			t.Fatal("accepted invalid state", invalid)
		}
	}
	after, _ := os.ReadFile(c.files().State("setup"))
	if string(before) != string(after) {
		t.Fatal("invalid write changed progress")
	}
	if err := os.WriteFile(c.files().State("setup"), []byte(`{"schemaVersion":1,"step":"direct","state":"active"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if legacy, err := c.setupProgress(); err != nil || legacy.Step != "direct" || legacy.FurthestStep != "" {
		t.Fatalf("legacy progress: %+v %v", legacy, err)
	}
	if err := os.WriteFile(c.files().State("setup"), []byte(`{"schemaVersion":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setupProgress(); err == nil {
		t.Fatal("unknown format silently accepted")
	}
}
