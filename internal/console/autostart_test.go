package console

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAutoStartSetting(t *testing.T) {
	c := testConsole(t)
	c.AutoStart()
	if c.DesktopState().CDN || c.service != nil {
		t.Fatal("CDN started with default settings")
	}
	for _, enabled := range []bool{true, false} {
		s := c.settings
		s.AutoStartCDN = enabled
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { loaded.Close() })
		loaded.videoAddress = "127.0.0.1:0"
		loaded.httpsAddress = "127.0.0.1:0"
		loaded.AutoStart()
		if loaded.settings.AutoStartCDN != enabled || loaded.DesktopState().CDN != enabled {
			t.Fatalf("saved autoStartCDN=%v was not honored", enabled)
		}
		loaded.Close()
	}
}

func TestAutoStartFailureKeepsConsoleAvailable(t *testing.T) {
	c := testConsole(t)
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
	manual.Header.Set("X-StepStash-Token", c.token)
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
