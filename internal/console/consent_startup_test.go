package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/legal"
)

func TestConsentReleasesAutomaticStartupOnce(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[enabled], func(t *testing.T) {
			c := testConsole(t)
			c.terms = termsReceipt{}
			c.settings.AutoAccelerate = enabled
			c.AutoStart()
			if c.httpServer != nil || !c.autoStartPending {
				t.Fatal("startup bypassed consent")
			}
			body, _ := json.Marshal(map[string]any{"version": legal.Version, "sha256": legal.Hash(), "agree": true, "contentRights": true})
			accept := func() {
				r := httptest.NewRequest("POST", "http://"+c.address+"/api/terms/accept", strings.NewReader(string(body)))
				r.Header.Set("X-SWD-Token", c.token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Error(w.Code, w.Body.String())
				}
			}
			// The consent response must finish while acceleration is waiting for
			// the lifecycle lock (as it could later wait for administrator consent).
			c.lifecycleMu.Lock()
			done := make(chan struct{})
			go func() { accept(); close(done) }()
			select {
			case <-done:
				c.lifecycleMu.Unlock()
			case <-time.After(time.Second):
				c.lifecycleMu.Unlock()
				t.Fatal("consent response waited for acceleration")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				c.mu.Lock()
				ready := !c.autoStartPending && (!enabled || c.activation.Phase == "waiting")
				c.mu.Unlock()
				if ready {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("consent did not honor startup preference")
				}
				time.Sleep(time.Millisecond)
			}
			postTaskAction(t, c, "stop")
			accept()
			c.AutoStart()
			if c.httpServer != nil {
				t.Fatal("repeated acceptance restarted stopped service")
			}
		})
	}
}

func TestRemovedSetupEndpointsAndReceiptsHaveNoEffects(t *testing.T) {
	c := testConsole(t)
	for _, path := range []string{"setup", "setup/validate", "setup/apply", "setup/receipt", "setup/folder", "setup/storage", "setup/logs", "setup/network", "setup/network/calibration/start"} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/"+path, strings.NewReader(`{}`))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	state := c.files().State("setup-apply")
	if err := os.MkdirAll(filepath.Dir(state), 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"id":"old","phase":"restart","importSource":"old-source"}`)
	if err := os.WriteFile(state, old, 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	c.settings.AutoAccelerate = true
	c.AutoStart()
	if c.httpServer == nil || c.importStatus.Running {
		t.Fatal("legacy receipt affected startup")
	}
	if data, err := os.ReadFile(state); err != nil || string(data) != string(old) {
		t.Fatal("legacy receipt changed", err)
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatal(w.Body.String())
	}
	for key := range status {
		if strings.HasPrefix(key, "setup") {
			t.Fatal("obsolete status field", key)
		}
	}
}
