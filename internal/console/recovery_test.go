package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestRecoveryServesEmbeddedUIWithoutBusinessAccess(t *testing.T) {
	view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{}, "configuration invalid")
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
	if page.Code != 200 || !strings.Contains(page.Body.String(), `"recovery":{"reason":"configuration invalid"}`) || !strings.Contains(page.Body.String(), `name="swd-token"`) {
		t.Fatalf("recovery page unavailable: %d %s", page.Code, page.Body.String())
	}
	if w := request("GET", "/api/status", "", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("invalid settings presented as active", w.Code)
	}
	if w := request("POST", "/api/activation/enable", view.token, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("recovery admitted a business mutation", w.Code)
	}
	if w := request("POST", "/api/exit", "wrong", ""); w.Code != http.StatusForbidden {
		t.Fatal("exit accepted an invalid token", w.Code)
	}
	if w := request("POST", "/api/exit", view.token, "http://example.invalid"); w.Code != http.StatusForbidden {
		t.Fatal("exit accepted another origin", w.Code)
	}
	if w := request("POST", "/api/exit", view.token, "http://127.0.0.1:12345"); w.Code != 200 {
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

func TestRecoveryExitsAfterLastClientLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{}, "configuration invalid")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go view.ExitWhenUnattended(ctx, time.Minute)
		attach := func() context.Context {
			client, stop := context.WithCancel(ctx)
			if !view.lifecycle.attach(client, stop) {
				t.Fatal("could not attach recovery client")
			}
			synctest.Wait()
			return client
		}
		assertRunning := func() {
			t.Helper()
			synctest.Wait()
			select {
			case <-view.ShutdownRequested():
				t.Fatal("recovery exited while attended or before grace expired")
			default:
			}
		}
		first, second := attach(), attach()
		time.Sleep(2 * time.Minute)
		assertRunning()
		view.lifecycle.detach(first)
		time.Sleep(2 * time.Minute)
		assertRunning()
		view.lifecycle.detach(second)
		synctest.Wait()
		time.Sleep(59 * time.Second)
		assertRunning()
		// A refresh or reopening the page cancels the old deadline.
		reopened := attach()
		time.Sleep(2 * time.Minute)
		assertRunning()
		view.lifecycle.detach(reopened)
		synctest.Wait()
		time.Sleep(59 * time.Second)
		assertRunning()
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-view.ShutdownRequested():
		default:
			t.Fatal("unattended recovery remained running")
		}
	})
}

func TestRecoveryExitsIfBrowserNeverConnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		view, err := NewRecovery("127.0.0.1:12345", desktop.Identity{}, "configuration invalid")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go view.ExitWhenUnattended(ctx, time.Minute)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-view.ShutdownRequested():
		default:
			t.Fatal("recovery remained running without any browser")
		}
	})
}
