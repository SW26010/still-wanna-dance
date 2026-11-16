package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func restoreConnectionRequest(c *Console) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/activation/restore", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestRestoreDirectConnectionStopsAccelerationWithoutChangingSettings(t *testing.T) {
	for _, tc := range []struct {
		name              string
		managed, prepared bool
		restoreOnExit     bool
	}{
		{"nothing to restore", false, false, false},
		{"reuse helper", true, true, false},
		{"prepare helper", true, false, false},
		{"default exit policy", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			// An unmarked API mapping belongs to the user and must survive.
			initial := hostsBaseline
			if tc.managed {
				initial, _ = transformHosts(initial, "enable")
			} else {
				initial += "127.0.0.1 api.udon.dance # user\r\n"
			}
			h, launches := fixtureHostsController(t, initial)
			c.editor.hosts = h
			c.settings.RestoreHostsOnStopOrExit = tc.restoreOnExit
			c.editor.initializeSettings(c.settings)
			if tc.prepared {
				if err := h.prepareRestore(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := c.start(); err != nil {
				t.Fatal(err)
			}
			settings, saved, engine := c.settings, c.editor.savedSettings, c.service
			configuration, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			c.batch = Batch{Running: true}
			for attempt := range 2 {
				w := restoreConnectionRequest(c)
				var result struct{ OK, Restored bool }
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.OK || result.Restored != (tc.managed && attempt == 0) {
					t.Fatal(w.Code, w.Body.String())
				}
				want := initial
				if tc.managed {
					want = hostsBaseline
				}
				if readHostsFixture(t, h.path) != want || *launches != map[bool]int{true: 1, false: 0}[tc.managed] {
					t.Fatal("wrong restoration or unexpected authorization", *launches)
				}
				if c.httpServer != nil || c.https != nil || c.videoListener != nil || c.queue.snapshot().Running || c.queueDesired || c.activation.Phase != "stopped" {
					t.Fatal("restoration left acceleration running")
				}
				if c.service != engine || !c.batch.Running || h.closed || c.settings != settings || c.editor.savedSettings != saved {
					t.Fatal("restoration changed settings, closed the helper or stopped independent work")
				}
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || string(after) != string(configuration) {
				t.Fatal("restoration changed persisted configuration", err)
			}
			c.batch = Batch{}
		})
	}
}

func TestRestoreDirectConnectionPreparationFailureLeavesAccelerationUnchanged(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		t.Run(map[bool]string{true: "canceled", false: "edited mapping"}[canceled], func(t *testing.T) {
			c := testConsole(t)
			owned, _ := transformHosts(hostsBaseline, "enable")
			if !canceled {
				owned = strings.ReplaceAll(owned, "127.0.0.1 api", "192.0.2.1 api")
			}
			h, launches := fixtureHostsController(t, owned)
			c.editor.hosts = h
			attempts, launch := 0, h.launch
			h.launch = func(ctx context.Context) (hostsConnection, error) {
				attempts++
				if canceled && attempts == 1 {
					return nil, errHostsAuthorizationCanceled
				}
				return launch(ctx)
			}
			if err := c.start(); err != nil {
				t.Fatal(err)
			}
			server, activation := c.httpServer, c.activation
			w := restoreConnectionRequest(c)
			var result struct{ Code string }
			if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &result) != nil || (result.Code == "uac_canceled") != canceled {
				t.Fatal(w.Code, w.Body.String())
			}
			if c.httpServer != server || c.activation != activation || readHostsFixture(t, h.path) != owned || *launches != 0 || attempts != map[bool]int{true: 1, false: 0}[canceled] {
				t.Fatal("preparation failure changed acceleration, hosts or authorization")
			}
			if canceled {
				if w = restoreConnectionRequest(c); w.Code != 200 || attempts != 2 || *launches != 1 || readHostsFixture(t, h.path) != hostsBaseline {
					t.Fatal("retry did not restore through one helper", w.Code, w.Body.String())
				}
			}
		})
	}
}

type failedDisableHosts struct{ hostsConnection }

func (h failedDisableHosts) request(r hostsRequest) error {
	if r.Action == "disable" {
		return errors.New("fixture restore failed")
	}
	return h.hostsConnection.request(r)
}

func TestRestoreDirectConnectionWriteFailureStillStopsAcceleration(t *testing.T) {
	c := testConsole(t)
	owned, _ := transformHosts(hostsBaseline, "enable")
	h, launches := fixtureHostsController(t, owned)
	c.editor.hosts = h
	launch := h.launch
	h.launch = func(ctx context.Context) (hostsConnection, error) {
		session, err := launch(ctx)
		return failedDisableHosts{session}, err
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	w := restoreConnectionRequest(c)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "fixture restore failed") || c.httpServer != nil || c.activation.ConnectionError == "" || readHostsFixture(t, h.path) != owned || *launches != 1 {
		t.Fatal("restore failure was hidden or changed hosts", w.Code, w.Body.String())
	}
}

func TestRestoreDirectConnectionFailureDoesNotPublishAuthorizationNotice(t *testing.T) {
	for _, action := range []string{"prepare", "disable"} {
		t.Run(action, func(t *testing.T) {
			c := testConsole(t)
			owned, _ := transformHosts(hostsBaseline, "enable")
			h, launches := fixtureHostsController(t, owned)
			c.editor.hosts = h
			launch := h.launch
			h.launch = func(ctx context.Context) (hostsConnection, error) {
				session, err := launch(ctx)
				if action == "prepare" {
					return failedPrepareHosts{session}, err
				}
				return failedDisableHosts{session}, err
			}
			w := restoreConnectionRequest(c)
			if w.Code != 400 || readHostsFixture(t, h.path) != owned || *launches != 1 {
				t.Fatal("unexpected restore failure", w.Code, w.Body.String(), *launches)
			}
			if h.status().AuthorizationID != "" {
				t.Fatal("uninstall recovery published a keep-connection suggestion after failure")
			}
		})
	}
}

func TestRestoreDirectConnectionRejectsUnauthorizedBusyAndClosing(t *testing.T) {
	c := testConsole(t)
	owned, _ := transformHosts(hostsBaseline, "enable")
	h, launches := fixtureHostsController(t, owned)
	c.editor.hosts = h
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("POST", "http://"+c.address+"/api/activation/restore", nil))
	if w.Code != 403 {
		t.Fatal("unauthorized restoration accepted", w.Code)
	}
	c.activationMu.Lock()
	w = restoreConnectionRequest(c)
	c.activationMu.Unlock()
	if w.Code != 400 {
		t.Fatal("concurrent restoration accepted", w.Code)
	}
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	w = restoreConnectionRequest(c)
	if w.Code != 400 || *launches != 0 || readHostsFixture(t, h.path) != owned {
		t.Fatal("rejected restoration changed hosts or authorized", w.Code, w.Body.String())
	}
}
