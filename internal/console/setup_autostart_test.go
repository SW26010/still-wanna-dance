package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupStartupConsole(t *testing.T, gateway, queue bool) *Console {
	t.Helper()
	root := t.TempDir()
	c, err := New(filepath.Join(root, "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	c.hosts, _ = fixtureHostsController(t, hostsBaseline)
	t.Cleanup(func() { c.Close() })
	c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
	if !c.settings.AutoAccelerate || !c.settings.QueueWithGateway {
		t.Fatal("new settings must enable both startup preferences")
	}
	c.settings.LogDir, c.settings.ManualLogDir = filepath.Join(root, "logs"), true
	c.settings.AutoAccelerate, c.settings.QueueWithGateway = gateway, queue
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c.AutoStart()
	assertSetupServices(t, c, false, false)
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	assertSetupServices(t, c, false, false)
	return c
}

func assertSetupServices(t *testing.T, c *Console, gateway, queue bool) {
	t.Helper()
	if got := c.DesktopState().CDN; got != gateway {
		t.Fatalf("gateway running = %v, want %v", got, gateway)
	}
	c.mu.Lock()
	running, desired := c.queue.Running, c.queueDesired
	c.mu.Unlock()
	if running != queue || desired != queue {
		t.Fatalf("queue running/desired = %v/%v, want %v", running, desired, queue)
	}
}

func applyStartupSetup(t *testing.T, c *Console, p setupPlan) SetupApplication {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/setup/apply", bytes.NewReader(body))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	var a SetupApplication
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatalf("apply: HTTP %d %s", w.Code, w.Body.String())
	}
	return a
}

func TestSetupReviewUsesActualRestartAndDeferredStartup(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("deferred=%v/restart=%v", deferred, restart), func(t *testing.T) {
				c := setupStartupConsole(t, true, true)
				if !deferred {
					completeStartupSetup(t, c)
					postTaskAction(t, c, "stop")
					postTaskAction(t, c, "queue/stop")
				}
				p := setupTestPlan(c)
				if restart {
					p.VideoStorageMode, p.MaxCacheBytes = "limited", 1<<30
				}
				effects, err := c.reviewSetupPlan(p)
				if err != nil || effects.RestartRequired != restart || effects.ApplyStartup != (restart || deferred) {
					t.Fatal(effects, err)
				}
				assertSetupServices(t, c, false, false)
			})
		}
	}
}

func TestSetupReviewDoesNotGuessCredentialChanges(t *testing.T) {
	c := setupStartupConsole(t, false, false)
	c.settings.SOCKS5Username, c.settings.SOCKS5Password = "user", "private-password"
	c.savedSettings = c.settings
	for _, password := range []string{"private-password", "different-password"} {
		p := setupTestPlan(c)
		p.Password = &password
		effects, err := c.reviewSetupPlan(p)
		if err != nil || effects.RestartRequired != (password != c.settings.SOCKS5Password) {
			t.Fatal("credential review", effects, err)
		}
		data, err := json.Marshal(effects)
		if err != nil || bytes.Contains(data, []byte("password")) {
			t.Fatal("review leaked a credential", string(data), err)
		}
	}
}

func TestSetupDeferredAutoStartHonorsPreferencesOnce(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, gateway := range []bool{false, true} {
			for _, queue := range []bool{false, true} {
				t.Run(fmt.Sprintf("restart=%v/gateway=%v/queue=%v", restart, gateway, queue), func(t *testing.T) {
					c := setupStartupConsole(t, gateway, queue)
					p := setupTestPlan(c)
					if restart {
						p.MaxCacheBytes = 25 << 30
						p.VideoStorageMode = "limited"
					}
					if _, err := c.validateSetupPlan(p); err != nil {
						t.Fatal(err)
					}
					assertSetupServices(t, c, false, false)
					a := applyStartupSetup(t, c, p)
					if restart {
						if a.Phase != "restart" {
							t.Fatalf("expected restart: %+v", a)
						}
						assertSetupServices(t, c, false, false)
						c = restartTestConsole(t, c)
						c.AutoStart()
						a = c.setupApplication
					}
					if a.Phase != "completed" || !a.ConfigSaved {
						t.Fatalf("application did not complete: %+v", a)
					}
					assertSetupServices(t, c, gateway, gateway && queue)
					if c.savedSettings.AutoAccelerate != gateway || c.savedSettings.QueueWithGateway != queue {
						t.Fatal("setup changed startup preferences")
					}
					postTaskAction(t, c, "stop")
					postTaskAction(t, c, "queue/stop")
					// Lost response/replay and a new wizard visit must both preserve
					// manual stops, even though the preferences remain enabled.
					applyStartupSetup(t, c, p)
					assertSetupServices(t, c, false, false)
					p = setupTestPlan(c)
					p.ID = "reentered-setup"
					if got := applyStartupSetup(t, c, p); got.Phase != "completed" {
						t.Fatalf("reentered: %+v", got)
					}
					assertSetupServices(t, c, false, false)
				})
			}
		}
	}
}

func TestSetupDoesNotRepeatNormalAutoStart(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	completeStartupSetup(t, c)
	c.AutoStart()
	assertSetupServices(t, c, true, true)
	postTaskAction(t, c, "stop")
	postTaskAction(t, c, "queue/stop")
	if a := applyStartupSetup(t, c, setupTestPlan(c)); a.Phase != "completed" {
		t.Fatalf("apply: %+v", a)
	}
	assertSetupServices(t, c, false, false)
}

func TestSetupDeferredAutoStartWaitsForSuccessfulApply(t *testing.T) {
	c := setupStartupConsole(t, true, true)
	p := setupTestPlan(c)
	p.UpstreamMode = ""
	if _, err := c.applySetupPlan(p); err == nil {
		t.Fatal("invalid plan accepted")
	}
	assertSetupServices(t, c, false, false)
	p = setupTestPlan(c)
	config, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.configPath, []byte(`{"schemaVersion":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if a := applyStartupSetup(t, c, p); a.Phase != "failed" || a.ConfigSaved {
		t.Fatalf("save failure: %+v", a)
	}
	assertSetupServices(t, c, false, false)
	if err := os.WriteFile(c.configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	p.ID = "retry-after-save-failure"
	if a := applyStartupSetup(t, c, p); a.Phase != "completed" {
		t.Fatalf("retry: %+v", a)
	}
	assertSetupServices(t, c, true, true)
}

func TestSetupDeferredAutoStartWaitsForSuccessfulContinuation(t *testing.T) {
	for _, failure := range []string{"none", "import", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			c := setupStartupConsole(t, true, true)
			version, err := c.setupSettingsVersion(c.settings)
			if err != nil {
				t.Fatal(err)
			}
			a := SetupApplication{ID: "continuation", Phase: "restart", ConfigSaved: true, TargetVersion: version, ImportSource: "fixture-source"}
			if err := c.writeSetupApplication(a); err != nil {
				t.Fatal(err)
			}
			c.resumeSetupApplication(func(string) error {
				assertSetupServices(t, c, false, false)
				if failure == "import" {
					return errors.New("import unavailable")
				}
				if failure == "none" {
					return nil
				}
				// Fail publication of the completed receipt after import starts.
				path := c.setupReceiptPath(a.ID)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				return nil
			})
			if failure == "none" {
				if c.setupApplication.Phase != "completed" || !c.setupApplication.ImportStarted {
					t.Fatalf("continuation: %+v", c.setupApplication)
				}
				assertSetupServices(t, c, true, true)
				return
			}
			if c.setupApplication.Phase != "failed" {
				t.Fatalf("continuation: %+v", c.setupApplication)
			}
			assertSetupServices(t, c, false, false)
			if a := applyStartupSetup(t, c, setupTestPlan(c)); a.Phase != "completed" {
				t.Fatalf("retry: %+v", a)
			}
			assertSetupServices(t, c, true, true)
		})
	}
}

func TestSetupDeferredAutoStartFailureIsNotRetriedByWizard(t *testing.T) {
	c := setupStartupConsole(t, true, true)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.httpsAddress = occupied.Addr().String()
	if a := applyStartupSetup(t, c, setupTestPlan(c)); a.Phase != "completed" {
		t.Fatalf("apply: %+v", a)
	}
	assertSetupServices(t, c, false, false)
	if c.cdnError == "" {
		t.Fatal("gateway startup error was not reported")
	}
	occupied.Close()
	postTaskAction(t, c, "queue/stop")
	p := setupTestPlan(c)
	p.ID = "reentered-after-start-failure"
	applyStartupSetup(t, c, p)
	assertSetupServices(t, c, false, false)
}

func TestSetupReentryWithRunningQueue(t *testing.T) {
	for _, change := range []string{"unchanged", "budget", "storage", "invalid"} {
		t.Run(change, func(t *testing.T) {
			c := setupStartupConsole(t, true, true)
			completeStartupSetup(t, c)
			assertSetupServices(t, c, true, true)
			engine, queueDone, active := c.service, c.queueDone, c.settings
			p := setupTestPlan(c)
			p.ID = "reentered-with-running-queue"
			switch change {
			case "budget":
				p.VideoStorageMode, p.MaxCacheBytes = "limited", 2<<30
			case "storage":
				p.StorageDir = filepath.Join(t.TempDir(), "new-library")
			case "invalid":
				p.UpstreamMode = ""
			}
			if change == "invalid" {
				if _, err := c.applySetupPlan(p); err == nil {
					t.Fatal("invalid plan accepted")
				}
				if c.savedSettings != active {
					t.Fatal("rejected plan changed saved settings")
				}
			} else {
				a := applyStartupSetup(t, c, p)
				want := "restart"
				if change == "unchanged" {
					want = "completed"
				}
				if a.Phase != want || !a.ConfigSaved {
					t.Fatalf("apply: %+v", a)
				}
				if replay := applyStartupSetup(t, c, p); replay.ID != a.ID || replay.Phase != a.Phase {
					t.Fatal("replayed apply changed its receipt", replay)
				}
			}
			assertSetupServices(t, c, true, true)
			if c.settings != active || c.service != engine || c.queueDone != queueDone {
				t.Fatal("wizard application changed the active engine or queue")
			}
			select {
			case <-queueDone:
				t.Fatal("queue stopped before restart")
			default:
			}
			if change == "unchanged" || change == "invalid" {
				return
			}
			c = restartTestConsole(t, c)
			select {
			case <-queueDone:
			default:
				t.Fatal("restart did not wait for the old queue")
			}
			c.AutoStart()
			assertSetupServices(t, c, true, true)
			if c.setupApplication.Phase != "completed" || c.settings.StorageDir != p.StorageDir || c.settings.VideoStorageMode != p.VideoStorageMode || c.settings.MaxCacheBytes != p.MaxCacheBytes {
				t.Fatal("restart did not complete the approved plan", c.setupApplication)
			}
		})
	}
}
