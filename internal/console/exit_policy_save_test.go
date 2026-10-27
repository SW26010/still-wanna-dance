package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/workfiles"
)

type failedPrepareHosts struct{ hostsConnection }

func (h failedPrepareHosts) request(r hostsRequest) error {
	if r.Action == "prepare" {
		return errors.New("fixture helper preparation failed")
	}
	return h.hostsConnection.request(r)
}

func TestExitPolicySavePreparesWithoutChangingHosts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		c := testConsole(t)
		next := c.settings
		next.RestoreHostsOnExit = false
		if err := c.save(next); err != nil {
			t.Fatal(err)
		}
		owned, _ := transformHosts(hostsBaseline, "enable")
		initial := owned
		if existing {
			initial = hostsBaseline
		}
		h, launches := fixtureHostsController(t, initial)
		c.hosts = h
		if existing {
			if err := h.change("enable", false); err != nil {
				t.Fatal(err)
			}
		}
		next.RestoreHostsOnExit = true
		if err := c.save(next); err != nil {
			t.Fatal(err)
		}
		if *launches != 1 || !h.prepared.Load() || !c.settings.RestoreHostsOnExit || c.settings != c.savedSettings || readHostsFixture(t, h.path) != owned {
			t.Fatal("save did not commit one prepared exit policy without changing hosts")
		}
		if err := c.Close(); err != nil || readHostsFixture(t, h.path) != hostsBaseline || *launches != 1 {
			t.Fatal("exit did not reuse prepared permissions", err)
		}
	}
}

func TestExitPolicySaveFailureLeavesConfigurationAndExitUnchanged(t *testing.T) {
	for _, failure := range []string{"uac", "helper", "config-write"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			next := c.settings
			next.RestoreHostsOnExit = false
			if err := c.save(next); err != nil {
				t.Fatal(err)
			}
			old := c.savedSettings
			before, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			owned, _ := transformHosts(hostsBaseline, "enable")
			h, launches := fixtureHostsController(t, owned)
			c.hosts = h
			launch := h.launch
			h.launch = func(ctx context.Context) (hostsConnection, error) {
				if failure == "uac" {
					return nil, errors.New("UAC canceled")
				}
				session, err := launch(ctx)
				if err == nil && failure == "helper" {
					return failedPrepareHosts{session}, nil
				}
				return session, err
			}
			if failure == "config-write" {
				if err := os.Mkdir(filepath.Join(filepath.Dir(c.configPath), workfiles.TempName(c.configPath)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			next.RestoreHostsOnExit = true
			next.PrepareAheadSeconds++
			if err := c.save(next); err == nil {
				t.Fatal("faulted save reported success")
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || string(after) != string(before) || c.settings != old || c.savedSettings != old {
				t.Fatal("failed save changed part of the configuration", err)
			}
			if err := c.Close(); err != nil || readHostsFixture(t, h.path) != owned {
				t.Fatal("failed save changed the original persistent exit", err)
			}
			if *launches > 1 {
				t.Fatal("exit requested new authorization")
			}
		})
	}
}

func TestExitPolicySaveWithoutManagedHostsDoesNotRequestPermission(t *testing.T) {
	c := testConsole(t)
	h, launches := fixtureHostsController(t, hostsBaseline+"127.0.0.1 api.udon.dance # user\r\n")
	c.hosts = h
	for _, restore := range []bool{false, true} {
		next := c.settings
		next.RestoreHostsOnExit = restore
		if err := c.save(next); err != nil || *launches != 0 {
			t.Fatal("saving without managed hosts requested permission", err)
		}
	}
}
