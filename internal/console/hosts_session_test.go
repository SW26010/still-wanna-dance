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

func TestHostsSessionChecksOwnedReadyMappingBeforeAuthorization(t *testing.T) {
	owned, _ := transformHosts(hostsBaseline, "enable")
	for _, tc := range []struct {
		name, initial       string
		restore, wantLaunch bool
	}{
		{"owned default", owned, true, true},
		{"owned persistent", owned, false, false},
		{"unowned default", hostsBaseline + "127.0.0.1 api.udon.dance # user\r\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, launches := fixtureHostsController(t, tc.initial)
			if err := h.change("enable", tc.restore); err != nil {
				t.Fatal(err)
			}
			if (*launches > 0) != tc.wantLaunch || h.prepared.Load() != tc.wantLaunch {
				t.Fatal("wrong authorization decision", *launches, h.prepared.Load())
			}
			if err := h.close(tc.restore); err != nil {
				t.Fatal(err)
			}
			want := tc.initial
			if tc.wantLaunch {
				want = hostsBaseline
			}
			if got := readHostsFixture(t, h.path); got != want {
				t.Fatalf("%q", got)
			}
		})
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
		if err := c.changeHosts("enable"); err != nil {
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
	go func() { done <- c.changeHosts("enable") }()
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

func TestExplicitGatewayStartPreparesExistingConnectionOnlyWhenNeeded(t *testing.T) {
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
		if (*launches == 1) != restore {
			t.Fatal("automatic acceleration did not prepare needed cleanup")
		}
		if err := c.startForUser(); err != nil {
			t.Fatal(err)
		}
		if (*launches == 1) != restore || h.status().ExitRestorePrepared != restore {
			t.Fatal("explicit start did not check cleanup capability", *launches)
		}
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
