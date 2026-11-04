package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestRecoveryServesEmbeddedUIWithoutBusinessAccess(t *testing.T) {
	view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:12345"+path, strings.NewReader(`{}`))
		req.Header.Set("X-SWD-Token", token)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		view.ServeHTTP(w, req)
		return w
	}
	page := request("GET", "/", "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), `"recovery":true`) || !strings.Contains(page.Body.String(), `name="swd-token"`) {
		t.Fatalf("recovery page unavailable: %d %s", page.Code, page.Body.String())
	}
	if w := request("GET", "/api/status", "", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("invalid settings presented as active", w.Code)
	}
	if w := request("POST", "/api/settings", view.console.token, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("recovery admitted a business mutation", w.Code)
	}
	if w := request("POST", "/api/exit", "wrong", ""); w.Code != http.StatusForbidden {
		t.Fatal("exit accepted an invalid token", w.Code)
	}
	if w := request("POST", "/api/exit", view.console.token, "http://example.invalid"); w.Code != http.StatusForbidden {
		t.Fatal("exit accepted another origin", w.Code)
	}
	if w := request("POST", "/api/exit", view.console.token, "http://127.0.0.1:12345"); w.Code != 200 {
		t.Fatal("recovery could not exit", w.Code, w.Body.String())
	}
	select {
	case <-view.ShutdownRequested():
	default:
		t.Fatal("exit was not delivered to the owner")
	}
	badHost := httptest.NewRequest("GET", "http://example.invalid/", nil)
	w := httptest.NewRecorder()
	view.ServeHTTP(w, badHost)
	if w.Code != http.StatusForbidden {
		t.Fatal("recovery accepted another host", w.Code)
	}
}

func TestRecoveryWithoutTrayExitsAfterLastClientLeaves(t *testing.T) {
	view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	client, cancelClient := context.WithCancel(t.Context())
	defer cancelClient()
	if !view.console.lifecycle.attach(client, cancelClient) {
		t.Fatal("could not attach recovery client")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go view.ExitWhenUnattended(ctx, 100*time.Millisecond)
	select {
	case <-view.ShutdownRequested():
		t.Fatal("closed while a browser was connected")
	case <-time.After(200 * time.Millisecond):
	}
	view.console.lifecycle.detach(client)
	select {
	case <-view.ShutdownRequested():
	case <-time.After(3 * time.Second):
		t.Fatal("unattended recovery remained running")
	}
}
