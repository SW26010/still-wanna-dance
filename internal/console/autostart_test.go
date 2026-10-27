package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAutoStartSetting(t *testing.T) {
	c := testConsole(t)
	c.AutoStart()
	if c.DesktopState().CDN || c.queue.Running || c.queueDesired || c.hosts.status().Ready {
		t.Fatal("default settings started acceleration or queue prefetch")
	}
	s := c.settings
	s.AutoAccelerate = true
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}

	c.AutoStart()
	if !c.DesktopState().CDN || !c.queue.Running || !c.queueDesired {
		t.Fatal("enabled automatic acceleration did not start both services")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	c.AutoStart() // repeated calls cannot restart the process attempt
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/", nil))
	if w.Code != 200 || c.DesktopState().CDN || c.queue.Running || c.queueDesired {
		t.Fatal("opening the web console restarted manually stopped services")
	}
	if !c.settings.AutoAccelerate || !c.settings.QueueWithGateway || !c.savedSettings.AutoAccelerate || !c.savedSettings.QueueWithGateway {
		t.Fatal("manual stop changed startup preferences")
	}
}

func TestAutoStartHonorsAccelerationAndQueueSettings(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, queue := range []bool{false, true} {
			t.Run(fmt.Sprintf("gateway=%v/queue=%v", gateway, queue), func(t *testing.T) {
				c := testConsole(t)
				if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
					t.Fatal(err)
				}
				s := c.settings
				s.AutoAccelerate, s.QueueWithGateway = gateway, queue
				if err := c.save(s); err != nil {
					t.Fatal(err)
				}
				if c.httpServer != nil || c.queue.Running || c.queueDesired {
					t.Fatal("saving startup preferences started services immediately")
				}
				c = restartTestConsole(t, c)

				c.AutoStart()
				if c.settings.AutoAccelerate != gateway || c.DesktopState().CDN != gateway || c.settings.QueueWithGateway != queue || c.queue.Running != (gateway && queue) || c.queueDesired != (gateway && queue) {
					t.Fatalf("saved preferences not honored: gateway=%v queue=%v desired=%v", c.DesktopState().CDN, c.queue.Running, c.queueDesired)
				}
			})
		}
	}
}

func TestAutoStartFailureKeepsConsoleAvailable(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.httpsAddress = occupied.Addr().String()
	c.settings.AutoAccelerate = true

	c.AutoStart()
	autoError := c.activation.Error
	if autoError == "" {
		t.Fatal("startup failure was not recorded")
	}
	manual := httptest.NewRequest("POST", "http://"+c.address+"/api/activation/enable", nil)
	manual.Header.Set("X-SWD-Token", c.token)
	response := httptest.NewRecorder()
	c.ServeHTTP(response, manual)
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || result["error"] != autoError || c.cdnError != autoError {
		t.Fatalf("automatic and manual start differ: auto=%q manual=%s", autoError, response.Body.String())
	}
	if c.DesktopState().CDN || c.videoListener != nil || c.https != nil {
		t.Fatal("failed auto start left CDN partially running")
	}
	if c.queue.Running || c.queueDesired || c.hosts.status().Ready {
		t.Fatal("failed acceleration left connection or queue running")
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "网页 HTTPS 转发启动失败") {
		t.Fatalf("console did not report startup failure: %d %s", w.Code, w.Body.String())
	}
	occupied.Close()
	if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
		t.Fatal(err)
	}
	if !c.DesktopState().CDN || c.cdnError != "" {
		t.Fatal("manual retry did not recover")
	}
}

func TestQueueWithGatewayFailureStillStartsGateway(t *testing.T) {
	c := testConsole(t) // The isolated log directory does not exist.
	c.settings.AutoAccelerate = true

	c.AutoStart()
	if !c.DesktopState().CDN || c.queue.Running || !c.queueDesired || c.actionErrors["queue"] == "" {
		t.Fatal("queue startup failure affected gateway or was not reported")
	}
}

func TestAutoAccelerationDeniedAuthorizationIsNotRetried(t *testing.T) {
	c := testConsole(t)
	c.settings.AutoAccelerate = true

	h, launches := fixtureHostsController(t, hostsBaseline)
	c.hosts = h
	launch := h.launch
	h.launch = func(ctx context.Context) (hostsConnection, error) { return nil, errors.New("fixture UAC canceled") }
	c.AutoStart()
	if c.httpServer != nil || c.activation.Phase != "failed" || !strings.Contains(c.activation.Error, "fixture UAC canceled") {
		t.Fatal(c.activation)
	}
	h.launch = launch
	c.AutoStart()
	if c.httpServer != nil || *launches != 0 {
		t.Fatal("automatic attempt retried")
	}
	if err := c.enableAcceleration(h.status, c.changeHostsLocked); err != nil || !h.status().Ready || *launches != 1 {
		t.Fatal("manual retry failed", err)
	}
}
