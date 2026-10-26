package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func lifecycleServer(t *testing.T) (*Console, *httptest.Server) {
	t.Helper()
	c := testConsole(t)
	server := httptest.NewUnstartedServer(c)
	c.address = server.Listener.Addr().String()
	server.Start()
	t.Cleanup(func() { _ = c.Close(); server.Close() })
	return c, server
}

func lifecycleClient(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/lifecycle", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readLifecycle(t *testing.T, conn *websocket.Conn, want ShutdownAction) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var value struct{ Action ShutdownAction }
	if err := wsjson.Read(ctx, conn, &value); err != nil || value.Action != want {
		t.Fatalf("lifecycle = %q, want %q: %v", value.Action, want, err)
	}
}

func TestLifecycleBroadcastAndLateSubscription(t *testing.T) {
	for _, action := range []ShutdownAction{ShutdownExit, ShutdownRestart} {
		t.Run(string(action), func(t *testing.T) {
			c, server := lifecycleServer(t)
			// Lifecycle does not require business consent and starts no services.
			c.mu.Lock()
			c.terms = termsReceipt{}
			c.mu.Unlock()
			clients := []*websocket.Conn{lifecycleClient(t, server), lifecycleClient(t, server)}
			for _, conn := range clients {
				readLifecycle(t, conn, "running")
			}
			c.lifecycle.request(action, nil)
			for _, conn := range clients {
				readLifecycle(t, conn, action)
			}
			readLifecycle(t, lifecycleClient(t, server), action)
			if c.httpServer != nil || c.service != nil {
				t.Fatal("lifecycle subscription started business services")
			}
		})
	}
}

func TestLifecycleOriginAndReadOnlyContract(t *testing.T) {
	c, server := lifecycleServer(t)
	for _, headers := range []http.Header{{}, {"Origin": {"http://evil.invalid"}}, {"Origin": {server.URL}, "Sec-Fetch-Site": {"cross-site"}}} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		conn, response, err := websocket.Dial(ctx, server.URL+"/api/lifecycle", &websocket.DialOptions{HTTPHeader: headers})
		cancel()
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("invalid origin accepted: %v %v", response, err)
		}
	}
	conn := lifecycleClient(t, server)
	readLifecycle(t, conn, "running")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, map[string]string{"action": "exit"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.Read(ctx)
	if err == nil || ctx.Err() != nil || c.ShutdownAction() != "running" {
		t.Fatalf("notification socket accepted a command: %v", err)
	}
	grace, stop := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer stop()
	started := time.Now()
	c.lifecycle.close(grace)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("rejected client delayed shutdown for %s", elapsed)
	}
}

func TestLifecycleImmediateShutdownKeepsFinalNotification(t *testing.T) {
	c, server := lifecycleServer(t)
	var clients []*websocket.Conn
	for range 24 {
		clients = append(clients, lifecycleClient(t, server))
	}
	c.RequestExit()
	c.BeginShutdown()
	for _, conn := range clients {
		// The initial snapshot may already contain the decision if the handler
		// was paused after the handshake. Either ordering must deliver exit.
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		var event struct{ Action ShutdownAction }
		err := wsjson.Read(ctx, conn, &event)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if event.Action == "running" {
			readLifecycle(t, conn, ShutdownExit)
		} else if event.Action != ShutdownExit {
			t.Fatalf("final notification = %q", event.Action)
		}
	}
}

func TestLifecycleUnplannedCloseAndSlowPeer(t *testing.T) {
	for _, planned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unplanned", true: "slow-peer"}[planned], func(t *testing.T) {
			c, server := lifecycleServer(t)
			conn := lifecycleClient(t, server)
			readLifecycle(t, conn, "running")
			if planned {
				c.RequestExit()
			}
			c.BeginShutdown()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			started := time.Now()
			c.lifecycle.close(ctx) // peer deliberately does not read or acknowledge
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("close with a 25ms grace blocked for %s", elapsed)
			}
			select {
			case <-c.lifecycle.drained:
			case <-time.After(time.Second):
				t.Fatal("upgraded connection survived shutdown")
			}
			if !planned {
				var event any
				if wsjson.Read(context.Background(), conn, &event) == nil || c.ShutdownAction() != "running" {
					t.Fatal("unplanned close reported an expected exit", event)
				}
			}
		})
	}
}

func TestLifecycleConcurrentRequestsShareAcknowledgedDecision(t *testing.T) {
	l := newApplicationLifecycle()
	entered, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.request(ShutdownRestart, func(action ShutdownAction) { close(entered); <-release })
	}()
	<-entered
	if l.snapshot() != "running" {
		t.Fatal("published before the winning acknowledgement finished")
	}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.request(ShutdownExit, func(action ShutdownAction) {
				if action != ShutdownRestart {
					t.Errorf("losing request acknowledged %s", action)
				}
			})
		}()
	}
	close(release)
	wg.Wait()
	if l.snapshot() != ShutdownRestart {
		t.Fatal("decision changed")
	}
}
