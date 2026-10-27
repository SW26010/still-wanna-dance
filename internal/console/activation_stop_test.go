package console

import (
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestAccelerationStopResumePolicies(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, queue := range []bool{false, true} {
			t.Run(fmt.Sprintf("restore=%v/queue=%v", restore, queue), func(t *testing.T) {
				c := testConsole(t)
				h, launches := fixtureHostsController(t, hostsBaseline)
				c.hosts = h
				c.settings.RestoreHostsOnExit, c.settings.QueueWithGateway = restore, queue
				// Pending restart settings must not change stop/resume behavior.
				c.savedSettings.RestoreHostsOnExit, c.savedSettings.QueueWithGateway = !restore, !queue
				if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
					t.Fatal(err)
				}
				for cycle := range 2 {
					if err := c.DesktopCommand(desktop.StartAcceleration); err != nil {
						t.Fatal(err)
					}
					if !h.status().Ready || c.httpServer == nil || c.queue.Running != queue || c.queueDesired != queue {
						t.Fatal("resume did not follow active policy")
					}
					engine, helper := c.service, h.session
					addresses := []string{c.videoListener.Addr().String(), c.https.listener.Addr().String()}
					late := c.beginVideoRequest(time.Now())
					c.batch = Batch{Running: true}
					if cycle == 0 {
						if err := c.DesktopCommand(desktop.StopAcceleration); err != nil {
							t.Fatal(err)
						}
					} else {
						r := httptest.NewRequest("POST", "http://"+c.address+"/api/activation/disable", nil)
						r.Header.Set("X-SWD-Token", c.token)
						w := httptest.NewRecorder()
						c.ServeHTTP(w, r)
						if w.Code != 200 {
							t.Fatal(w.Code, w.Body.String())
						}
					}
					late()
					if c.httpServer != nil || c.https != nil || c.videoListener != nil || c.queue.Running || c.queueDesired || c.activation.Phase != "stopped" || !c.activation.FirstRequest.IsZero() {
						t.Fatal("stop left gateway, queue or request observation running")
					}
					for _, address := range addresses {
						listener, err := net.Listen("tcp4", address)
						if err != nil {
							t.Fatal("stop did not release listener", err)
						}
						listener.Close()
					}
					if (readHostsFixture(t, h.path) == hostsBaseline) != restore || *launches != 1 || h.session != helper || h.closed || c.service != engine || !c.batch.Running {
						t.Fatal("wrong hosts policy, helper replaced, or independent work stopped")
					}
					if err := c.disableAcceleration(); err != nil || *launches != 1 {
						t.Fatal("repeated stop changed authorization", err)
					}
					c.batch = Batch{}
				}
			})
		}
	}
}

func TestAccelerationStopRestoreFailureStillStopsGateway(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-helper=%v", missing), func(t *testing.T) {
			c := testConsole(t)
			owned, _ := transformHosts(hostsBaseline, "enable")
			h, launches := fixtureHostsController(t, owned)
			c.hosts = h
			if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := c.start(); err != nil {
				t.Fatal(err)
			}
			if !missing {
				if err := h.change("enable", true); err != nil {
					t.Fatal(err)
				}
				owned = strings.ReplaceAll(owned, "127.0.0.1 api", "192.0.2.1 api")
				if err := os.WriteFile(h.path, []byte(owned), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := *launches
			err := c.disableAcceleration()
			if err == nil || !strings.Contains(err.Error(), "未能恢复游戏直连") || c.activation.Error != err.Error() || c.activation.ConnectionError != err.Error() || c.activation.Phase != "stopped" || c.httpServer != nil || c.queue.Running || c.queueDesired || readHostsFixture(t, h.path) != owned || *launches != before {
				t.Fatal("stop hid failure, changed hosts or requested authorization", err)
			}
			// Repair the private fixture and retry through the same helper.
			if !missing {
				original, _ := transformHosts(hostsBaseline, "enable")
				if err := os.WriteFile(h.path, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
				if err := c.disableAcceleration(); err != nil || c.activation.Error != "" || c.activation.ConnectionError != "" || *launches != before {
					t.Fatal("stop could not recover through existing helper", err)
				}
			}
		})
	}
}

func TestAccelerationStopEndpointRequiresAuthorization(t *testing.T) {
	c := testConsole(t)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("POST", "http://"+c.address+"/api/activation/disable", nil))
	if w.Code != 403 || c.activation.Phase != "" {
		t.Fatal("unauthorized stop was accepted")
	}
}
