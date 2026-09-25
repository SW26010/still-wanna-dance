package console

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"stepstash/internal/desktop"
)

func TestDesktopSharesWebRuntime(t *testing.T) {
	c := testConsole(t)
	if err := c.DesktopCommand(desktop.ToggleCDN); err != nil {
		t.Fatal(err)
	}
	if !c.DesktopState().CDN {
		t.Fatal("tray did not start web engine")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	if c.DesktopState().CDN {
		t.Fatal("tray state stale after web stop")
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.DesktopCommand(desktop.ToggleCDN); err != nil {
		t.Fatal(err)
	}
	if c.DesktopState().CDN {
		t.Fatal("tray did not stop web engine")
	}
	c.Close()
	if err := c.DesktopCommand(desktop.ToggleCDN); err == nil {
		t.Fatal("restarted while closing")
	}
}

func TestConsoleIdentityIsHostProtected(t *testing.T) {
	c := testConsole(t)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/identity", nil))
	var id desktop.Identity
	if err := json.Unmarshal(w.Body.Bytes(), &id); err != nil || id != desktop.AppIdentity {
		t.Fatal(id, err)
	}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://untrusted.test/api/identity", nil))
	if w.Code != 403 {
		t.Fatal("identity accepted foreign Host")
	}
}
