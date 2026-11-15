package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRealtimeHistoryAndBlockSockets(t *testing.T) {
	for _, path := range []string{"/api/history/events", "/api/block-demands/events", "/api/block-demands/events?block=missing/0"} {
		t.Run(path, func(t *testing.T) {
			c := testConsole(t)
			var active atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				active.Add(1)
				defer active.Add(-1)
				c.ServeHTTP(w, r)
			}))
			c.address = server.Listener.Addr().String()
			server.Start()
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			dial := func() *websocket.Conn {
				t.Helper()
				conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+path, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.CloseNow() })
				return conn
			}
			read := func(conn *websocket.Conn, want string) json.RawMessage {
				t.Helper()
				var frame struct {
					Event string
					Data  json.RawMessage
				}
				if err := wsjson.Read(ctx, conn, &frame); err != nil {
					t.Fatal(err)
				}
				if frame.Event != want {
					t.Fatalf("event %q, want %q", frame.Event, want)
				}
				return frame.Data
			}
			conn := dial()
			kind := "blocks"
			if strings.Contains(path, "history") {
				kind = "history"
			}
			read(conn, kind)
			c.mu.Lock()
			started := c.service != nil
			c.mu.Unlock()
			if started {
				t.Fatal("subscription created an engine")
			}
			if kind == "blocks" {
				c.lifecycleMu.Lock()
				_, err := c.ensureEngineLocked()
				c.lifecycleMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(path, "?block=") {
					kind = "block-detail"
				}
				read(conn, kind) // Engine startup continues on the same socket.
			}
			// A data frame terminates the read-only transport without an action.
			if err := wsjson.Write(ctx, conn, json.RawMessage(`{"action":"exit"}`)); err != nil {
				t.Fatal(err)
			}
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal("data message did not close stream promptly")
			}
			c.mu.Lock()
			closing := c.closing
			c.mu.Unlock()
			if closing {
				t.Fatal("subscription executed an action")
			}
			conn = dial()
			read(conn, kind) // Reconnection gets a fresh snapshot/detail.
			c.BeginShutdown()
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal("shutdown did not close stream promptly")
			}
			deadline := time.Now().Add(time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("subscription handler retained after close")
			}
			c.mu.Lock()
			waiting := len(c.blockEngineWaiters)
			c.mu.Unlock()
			if waiting != 0 {
				t.Fatal("subscription retained engine waiter")
			}
		})
	}
}
