package console

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hostsBaseline = "# private fixture\r\n127.0.0.1 localhost\r\n127.0.0.1 play.udon.dance # user\r\n"

func hostsFixture(t *testing.T, initial string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readHostsFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fixtureHostsController(t *testing.T, initial string) (*hostsController, *int) {
	t.Helper()
	path, launches := hostsFixture(t, initial), 0
	h := newHostsController(path, func(context.Context) (hostsConnection, error) {
		launches++
		parent, helper := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- serveHostsSession(helper, path, func() {}) }()
		return newHostsClient(parent, func() error { return <-done }), nil
	})
	t.Cleanup(func() { h.close(true) })
	return h, &launches
}

func TestHostsSessionReuseAndExitPolicy(t *testing.T) {
	seenAuthorizations := make(map[string]bool)
	for _, restore := range []bool{true, false} {
		t.Run(map[bool]string{true: "restore", false: "persist"}[restore], func(t *testing.T) {
			h, launches := fixtureHostsController(t, hostsBaseline)
			// No-op recovery and inspection must not start an elevated process.
			if err := h.change("disable", restore); err != nil || *launches != 0 {
				t.Fatal(err, *launches)
			}
			if h.status().AuthorizationID != "" {
				t.Fatal("no-op operation reported authorization")
			}
			var authorizationID string
			for _, action := range []string{"enable", "enable", "disable", "enable"} {
				if err := h.change(action, restore); err != nil {
					t.Fatal(action, err)
				}
				id := h.status().AuthorizationID
				if id == "" || authorizationID != "" && id != authorizationID {
					t.Fatal("successful authorization missing or changed while reusing helper")
				}
				authorizationID = id
			}
			if *launches != 1 || !inspectHosts(readHostsFixture(t, h.path)).Ready {
				t.Fatal("operations did not share a single helper", *launches)
			}
			if seenAuthorizations[authorizationID] {
				t.Fatal("different helper sessions shared an authorization identity")
			}
			seenAuthorizations[authorizationID] = true
			for range 2 {
				if err := h.close(restore); err != nil {
					t.Fatal(err)
				}
			}
			if got := readHostsFixture(t, h.path); (got == hostsBaseline) != restore || *launches != 1 {
				t.Fatalf("restore=%v launches=%d hosts=%q", restore, *launches, got)
			}
			if err := h.change("enable", restore); err == nil || *launches != 1 {
				t.Fatal("shutdown allowed new elevation", err)
			}
		})
	}
}

func TestHostsSessionReusesReadyMappingWithoutAuthorization(t *testing.T) {
	owned, _ := transformHosts(hostsBaseline, "enable")
	for _, tc := range []struct {
		name, initial string
		restore       bool
	}{
		{"owned default", owned, true},
		{"owned persistent", owned, false},
		{"unowned default", hostsBaseline + "127.0.0.1 api.udon.dance # user\r\n", true},
		{"unowned API with unrelated marker", hostsBaseline + "127.0.0.1 api.udon.dance # user\r\n127.0.0.1 nya.xin.moe " + marker + "\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, launches := fixtureHostsController(t, tc.initial)
			if err := h.change("enable", tc.restore); err != nil {
				t.Fatal(err)
			}
			if *launches != 0 || h.prepared.Load() {
				t.Fatal("wrong authorization decision", *launches, h.prepared.Load())
			}
			if err := h.close(tc.restore); err != nil {
				t.Fatal(err)
			}
			if got := readHostsFixture(t, h.path); got != tc.initial {
				t.Fatalf("%q", got)
			}
		})
	}
}

func TestHostsSessionRestoresAPIAlongsideUnmanagedEntries(t *testing.T) {
	original := hostsBaseline + "\r\n127.0.0.1 nya.xin.moe " + marker + "\r\n# SWD managed: unrelated note\r\n"
	h, launches := fixtureHostsController(t, original)
	if h.status().Managed {
		t.Error("unrelated entries reported as owned API mappings")
	}
	if err := h.prepareRestore(); err != nil || *launches != 0 {
		t.Error("unrelated entries requested restoration authorization", err, *launches)
	}
	if err := h.change("enable", true); err != nil {
		t.Fatal(err)
	}
	if status := h.status(); !status.Ready || !status.Managed || !status.ExitRestorePrepared {
		t.Fatal("API connection not ready for restoration", status)
	}
	if err := h.close(true); err != nil {
		t.Fatal("unrelated entries blocked API restoration", err)
	}
	if got := readHostsFixture(t, h.path); got != original || *launches != 1 {
		t.Fatalf("restoration changed unrelated entries or requested new authorization: %q, launches=%d", got, *launches)
	}
}

func TestHostsSessionConflictAndDeniedAuthorizationNeverChangeFile(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		data, calls := hostsBaseline, 0
		if conflict {
			data += "192.0.2.1 api.udon.dance\r\n"
		}
		h := newHostsController(hostsFixture(t, data), func(context.Context) (hostsConnection, error) {
			calls++
			return nil, errors.New("UAC canceled")
		})
		if err := h.change("enable", true); err == nil {
			t.Fatal("accepted failed activation")
		}
		if err := h.close(true); err != nil {
			t.Fatal(err)
		}
		if calls != map[bool]int{true: 0, false: 1}[conflict] || readHostsFixture(t, h.path) != data {
			t.Fatal("preflight/exit launched unexpectedly or modified hosts", calls)
		}
		if h.status().AuthorizationID != "" {
			t.Fatal("failed authorization reported success")
		}
	}
}

func TestHostsHelperDisconnectUsesSameExitPolicy(t *testing.T) {
	for _, restore := range []bool{true, false} {
		h, launches := fixtureHostsController(t, hostsBaseline)
		if err := h.change("enable", restore); err != nil {
			t.Fatal(err)
		}
		// Main process lost its connection, without sending a close command.
		if err := h.session.close(); err != nil {
			t.Fatal(err)
		}
		if (readHostsFixture(t, h.path) == hostsBaseline) != restore {
			t.Fatal("wrong disconnect policy")
		}
		if err := h.close(true); err == nil {
			t.Fatal("lost helper must not report a confirmed cleanup")
		}
		if *launches != 1 {
			t.Fatal("exit attempted new authorization")
		}
	}
}

func TestHostsExitReportsChangedEntryWithoutElevationOrOverwriting(t *testing.T) {
	h, launches := fixtureHostsController(t, hostsBaseline)
	if err := h.change("enable", true); err != nil {
		t.Fatal(err)
	}
	edited := strings.ReplaceAll(readHostsFixture(t, h.path), "127.0.0.1 api.udon.dance", "192.0.2.1 api.udon.dance")
	if err := os.WriteFile(h.path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.close(true); err == nil || *launches != 1 || readHostsFixture(t, h.path) != edited {
		t.Fatal("edited entry was overwritten or exit elevated", err, *launches)
	}
}

func TestHostsHelperRejectsUnsupportedActionsAndTerms(t *testing.T) {
	h, launches := fixtureHostsController(t, hostsBaseline)
	var err error
	h.session, err = h.launch(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []hostsRequest{{Action: "run-command"}, {Action: "enable", TermsVersion: "old"}} {
		if err := h.session.request(request); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if readHostsFixture(t, h.path) != hostsBaseline || *launches != 1 {
		t.Fatal("invalid operation changed file")
	}
}

func TestHostsConsoleCloseUsesPreparedHelperAndActiveSettings(t *testing.T) {
	for _, restore := range []bool{true, false} {
		c := testConsole(t)
		h, launches := fixtureHostsController(t, hostsBaseline)
		c.hosts = h
		c.settings.RestoreHostsOnExit = restore
		// An unsaved synthetic snapshot must not change the committed active policy.
		c.savedSettings.RestoreHostsOnExit = !restore
		if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if (readHostsFixture(t, h.path) == hostsBaseline) != restore || *launches != 1 {
			t.Fatal("Console.Close did not obey the active policy")
		}
	}
}

func TestRestoreHostsSettingDefaultsAndImmediateSave(t *testing.T) {
	for _, field := range []string{"", `,"restoreHostsOnExit":null`, `,"restoreHostsOnExit":true`, `,"restoreHostsOnExit":false`} {
		config := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(config, []byte(`{"schemaVersion":7`+field+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:18081")
		if err != nil {
			t.Fatal(err)
		}
		c.hosts, _ = fixtureHostsController(t, hostsBaseline)
		want := !strings.Contains(field, "false")
		if c.settings.RestoreHostsOnExit != want {
			t.Fatal("wrong default", field)
		}
		next := c.savedSettings
		next.RestoreHostsOnExit = !want
		if err := c.save(next); err != nil {
			t.Fatal(err)
		}
		if c.settings.RestoreHostsOnExit == want {
			t.Fatal("save did not change live exit policy")
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		c, err = newConsoleWithWorkForTest(t, config, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if c.settings.RestoreHostsOnExit == want {
			t.Fatal("saved policy was not loaded")
		}
		c.Close()
	}
}

func TestRestoreHostsSettingsAPIDefaultAndExplicitFalse(t *testing.T) {
	c := testConsole(t)
	for _, include := range []bool{false, true} {
		data, _ := json.Marshal(c.settings)
		var input map[string]any
		json.Unmarshal(data, &input)
		delete(input, "restoreHostsOnExit")
		if include {
			input["restoreHostsOnExit"] = false
		}
		data, _ = json.Marshal(input)
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(string(data)))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 || c.savedSettings.RestoreHostsOnExit == include || c.settings.RestoreHostsOnExit == include {
			t.Fatal(w.Code, w.Body.String(), c.savedSettings.RestoreHostsOnExit)
		}
	}
}

func TestHostsShutdownCancelsPendingAuthorization(t *testing.T) {
	c := testConsole(t)
	entered := make(chan struct{})
	calls := 0
	c.hosts = newHostsController(hostsFixture(t, hostsBaseline), func(ctx context.Context) (hostsConnection, error) {
		calls++
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- c.enableAcceleration(c.hosts.status, c.changeHostsLocked) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for pending UAC instead of canceling startup")
	}
	if err := <-done; err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if readHostsFixture(t, c.hosts.path) != hostsBaseline {
		t.Fatal("pending authorization changed hosts")
	}
}

func TestAccelerationReusesExistingConnectionWithoutPreparingRestore(t *testing.T) {
	for _, restore := range []bool{true, false} {
		c := testConsole(t)
		owned, _ := transformHosts(hostsBaseline, "enable")
		h, launches := fixtureHostsController(t, owned)
		c.hosts = h
		c.settings.RestoreHostsOnExit = restore
		c.settings.AutoAccelerate = true
		c.settings.QueueWithGateway = false
		c.savedSettings = c.settings

		c.AutoStart()
		if *launches != 0 || c.httpServer == nil {
			t.Fatal("automatic acceleration requested permission or did not start")
		}
		if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
			t.Fatal(err)
		}
		if *launches != 0 || h.status().ExitRestorePrepared {
			t.Fatal("explicit start prepared cleanup", *launches)
		}
	}
}

func TestHostsRestorePreparationEndpointDoesNotControlAcceleration(t *testing.T) {
	for _, failure := range []string{"none", "canceled", "preflight"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			owned, _ := transformHosts(hostsBaseline, "enable")
			h, launches := fixtureHostsController(t, owned)
			c.hosts = h
			launch := h.launch
			attempts := 0
			h.launch = func(ctx context.Context) (hostsConnection, error) {
				attempts++
				if failure == "canceled" && attempts == 1 {
					return nil, errors.Join(errors.New("fixture authorization"), errHostsAuthorizationCanceled)
				}
				session, err := launch(ctx)
				if failure == "preflight" && err == nil {
					return failedPrepareHosts{session}, nil
				}
				return session, err
			}
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "http://"+c.address+"/api/hosts/prepare", nil)
				r.Header.Set("X-SWD-Token", c.token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				return w
			}
			c.AutoStart() // Automatic acceleration is off; startup must not prompt.
			if attempts != 0 || c.httpServer != nil {
				t.Fatal("startup requested authorization or enabled acceleration")
			}
			w := request()
			if w.Code != 200 {
				var response map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if (response["code"] == "uac_canceled") != (failure == "canceled") {
					t.Fatal("wrong cancellation classification", response)
				}
			}
			if (w.Code == 200) != (failure == "none") || h.prepared.Load() != (failure == "none") || attempts != 1 || c.httpServer != nil || readHostsFixture(t, h.path) != owned {
				t.Fatal("repair changed hosts, enabled acceleration or hid failure", w.Code, w.Body.String())
			}
			if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil || c.httpServer == nil || attempts != 1 {
				t.Fatal("acceleration retried or depended on repair", err, attempts)
			}
			server, activation := c.httpServer, c.activation
			if session, ok := h.session.(failedPrepareHosts); ok {
				h.session = session.hostsConnection
			}
			if w = request(); w.Code != 200 || !h.prepared.Load() || *launches != 1 || c.httpServer != server || c.activation != activation || readHostsFixture(t, h.path) != owned {
				t.Fatal("repair retry changed acceleration or did not prepare cleanup", w.Code, w.Body.String())
			}
			if err := c.Close(); err != nil || readHostsFixture(t, h.path) != hostsBaseline || *launches != 1 {
				t.Fatal("exit did not reuse restoration permission", err)
			}
		})
	}
}

func TestHostsRepairClearsOnlyResolvedPermissionError(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing permission", true: "edited mapping"}[unrelated], func(t *testing.T) {
			c := testConsole(t)
			owned, _ := transformHosts(hostsBaseline, "enable")
			h, _ := fixtureHostsController(t, owned)
			c.hosts = h
			if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
				t.Fatal(err)
			}
			c.beginVideoRequest(time.Now())()
			if unrelated {
				if err := h.prepareRestore(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.path, []byte(strings.ReplaceAll(owned, "127.0.0.1 api", "192.0.2.1 api")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.disableAcceleration(); err == nil || c.activation.ConnectionError == "" {
				t.Fatal("stop did not record its restoration failure")
			}
			if err := os.WriteFile(h.path, []byte(owned), 0600); err != nil {
				t.Fatal(err)
			}
			before, generation := c.activation, c.activationGeneration
			c.cdnError = "unrelated gateway error"
			c.recordActionError("/api/queue/start", errors.New("unrelated queue error"))
			if !unrelated {
				launch := h.launch
				h.launch = func(context.Context) (hostsConnection, error) { return nil, errHostsAuthorizationCanceled }
				if err := c.prepareHostsRestore(); err == nil || c.activation != before {
					t.Fatal("failed repair cleared the unresolved permission error", err)
				}
				h.launch = launch
			}
			if err := c.prepareHostsRestore(); err != nil {
				t.Fatal(err)
			}
			if (c.activation.ConnectionError != "") != unrelated {
				t.Fatalf("resolved permission error survived or unrelated error disappeared: %q", c.activation.ConnectionError)
			}
			if c.activation.Phase != before.Phase || c.activation.Error != before.Error || c.activation.Started != before.Started || c.activation.ReadyAt != before.ReadyAt || c.activation.FirstRequest != before.FirstRequest || c.activationGeneration != generation || c.cdnError != "unrelated gateway error" || c.actionErrors["queue"] != "unrelated queue error" || c.httpServer != nil || readHostsFixture(t, h.path) != owned {
				t.Fatal("repair changed observation, history, other errors, gateway or hosts")
			}
		})
	}
}

func TestHostsClientRetiresUncertainReplyStream(t *testing.T) {
	parent, helper := net.Pipe()
	defer helper.Close()
	c := newHostsClient(parent, nil)
	defer c.close()
	go func() {
		scan := hostsScanner(helper)
		if scan.Scan() {
			helper.Write([]byte("invalid-json\n"))
		}
	}()
	first := c.request(hostsRequest{Action: "enable"})
	if first == nil {
		t.Fatal("invalid reply accepted")
	}
	// Must return the original failure immediately, without sending another
	// command or consuming a delayed response as the shutdown acknowledgement.
	if next := c.request(hostsRequest{Action: "close"}); next != first {
		t.Fatal("uncertain stream was reused", next)
	}
}
