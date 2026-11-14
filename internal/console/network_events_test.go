package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestNetworkEventsSnapshotsAndCancellation(t *testing.T) {
	c := testConsole(t)
	started := stubNetworkDiagnostics(t, c)
	server := httptest.NewUnstartedServer(c)
	c.address = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/network/events", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	read := func(conn *websocket.Conn, want string, running bool) {
		t.Helper()
		var frame struct {
			Event string `json:"event"`
			Data  struct {
				Revision uint64 `json:"revision"`
				Running  bool   `json:"running"`
			} `json:"data"`
		}
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Event != want || frame.Data.Running != running {
			t.Fatalf("unexpected network frame: %+v", frame)
		}
	}
	conn := dial()
	read(conn, "connectivity", false)
	read(conn, "calibration", false)
	select {
	case <-started:
		t.Fatal("subscription started a network check")
	default:
	}
	if err := c.controlNetworkConnectivity(false); err != nil {
		t.Fatal(err)
	}
	<-started
	read(conn, "connectivity", true)
	conn.CloseNow()
	// Reconnection obtains current snapshots instead of replaying stale data.
	conn = dial()
	read(conn, "connectivity", true)
	read(conn, "calibration", false)
	// The notification transport cannot accept management commands.
	if err := wsjson.Write(ctx, conn, json.RawMessage(`{"action":"cancel"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("read-only stream did not reject a command promptly: %v", err)
	}
	state, _ := c.networkDiagnostics.connectivitySnapshot()
	if !state.Running {
		t.Fatal("notification message canceled a diagnostic")
	}
	conn = dial()
	read(conn, "connectivity", true)
	read(conn, "calibration", false)
	c.BeginShutdown()
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("shutdown did not close network socket: %v", err)
	}
}

func TestNetworkEventsAdmission(t *testing.T) {
	c := testConsole(t)
	server := httptest.NewUnstartedServer(c)
	c.address = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for _, headers := range []http.Header{nil, {"Origin": {"http://untrusted.invalid"}}, {"Origin": {server.URL}, "Sec-Fetch-Site": {"cross-site"}}} {
		conn, response, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/network/events", &websocket.DialOptions{HTTPHeader: headers})
		if conn != nil {
			conn.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("invalid network event origin accepted: %v %v", response, err)
		}
	}
	c.mu.Lock()
	c.terms = termsReceipt{}
	c.mu.Unlock()
	conn, response, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/network/events", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if conn != nil {
		conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("unaccepted terms opened network events: %v %v", response, err)
	}
}
