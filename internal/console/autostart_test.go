package console

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAutoStartSetting(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.AutoStart()
	if !c.DesktopState().CDN || !c.queue.Running || !c.queueDesired {
		t.Fatal("default settings did not start both services")
	}
	postTaskAction(t, c, "stop")
	postTaskAction(t, c, "queue/stop")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/", nil))
	if w.Code != 200 || c.DesktopState().CDN || c.queue.Running || c.queueDesired {
		t.Fatal("opening the web console restarted manually stopped services")
	}
	if !c.settings.AutoStartCDN || !c.settings.AutoStartQueue || !c.savedSettings.AutoStartCDN || !c.savedSettings.AutoStartQueue {
		t.Fatal("manual stop changed startup preferences")
	}
}

func TestAutoStartHonorsIndependentSavedSettings(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		for _, queue := range []bool{false, true} {
			t.Run(fmt.Sprintf("gateway=%v/queue=%v", gateway, queue), func(t *testing.T) {
				c := testConsole(t)
				if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
					t.Fatal(err)
				}
				s := c.settings
				s.AutoStartCDN, s.AutoStartQueue = gateway, queue
				if err := c.save(s); err != nil {
					t.Fatal(err)
				}
				if c.httpServer != nil || c.queue.Running || c.queueDesired {
					t.Fatal("saving startup preferences started services immediately")
				}
				c = restartTestConsole(t, c)
				c.AutoStart()
				if c.settings.AutoStartCDN != gateway || c.DesktopState().CDN != gateway || c.settings.AutoStartQueue != queue || c.queue.Running != queue || c.queueDesired != queue {
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
	c.settings.AutoStartCDN = true
	c.AutoStart()
	autoError := c.cdnError
	if autoError == "" {
		t.Fatal("startup failure was not recorded")
	}
	manual := httptest.NewRequest("POST", "http://"+c.address+"/api/start", nil)
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
	if !c.queue.Running || !c.queueDesired {
		t.Fatal("gateway startup failure stopped queue prefetch")
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "网页 HTTPS 转发启动失败") {
		t.Fatalf("console did not report startup failure: %d %s", w.Code, w.Body.String())
	}
	occupied.Close()
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if !c.DesktopState().CDN || c.cdnError != "" {
		t.Fatal("manual retry did not recover")
	}
}

func TestAutoStartQueueFailureStillStartsGateway(t *testing.T) {
	c := testConsole(t) // The isolated log directory does not exist.
	c.AutoStart()
	if !c.DesktopState().CDN || c.queue.Running || !c.queueDesired || c.actionErrors["queue"] == "" {
		t.Fatal("queue startup failure affected gateway or was not reported")
	}
}
