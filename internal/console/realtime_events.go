package console

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type realtimeEvents struct {
	ctx   context.Context
	write func(event string, revision uint64, data any) error
	close func()
}

// WebUI uses WebSocket so visible windows cannot exhaust HTTP/1 request slots.
// Existing observation tools may still request SSE; both use the same producer.
// Callers check business admission before opening either transport.
func (c *Console) openRealtimeEvents(w http.ResponseWriter, r *http.Request) *realtimeEvents {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		return &realtimeEvents{ctx: r.Context(), close: func() {}, write: func(event string, revision uint64, data any) error {
			return writeServerEvent(w, controller, event, revision, data)
		}}
	}
	return c.openRealtimeSocket(w, r)
}

func (c *Console) openRealtimeSocket(w http.ResponseWriter, r *http.Request) *realtimeEvents {
	if r.Header.Get("Origin") != "http://"+c.address {
		http.Error(w, "invalid event origin", http.StatusForbidden)
		return nil
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		// Read control frames and detect peer closure. Data messages terminate
		// this read-only subscription and cannot execute management actions.
		defer cancel()
		_, _, _ = conn.Reader(ctx)
	}()
	return &realtimeEvents{ctx: ctx, close: func() { cancel(); conn.CloseNow() }, write: func(event string, _ uint64, data any) error {
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return wsjson.Write(writeCtx, conn, struct {
			Event string `json:"event"`
			Data  any    `json:"data"`
		}{event, data})
	}}
}

func (c *Console) serveRealtimeEvents(w http.ResponseWriter, r *http.Request, event string, snapshot func() diagnosticEventSnapshot) {
	stream := c.openRealtimeEvents(w, r)
	if stream == nil {
		return
	}
	defer stream.close()
	for {
		s := snapshot()
		if err := stream.write(event, s.revision, s.state); err != nil || s.closed {
			return
		}
		select {
		case <-stream.ctx.Done():
			return
		case <-c.lifecycle.stop:
			return
		case <-s.changed:
		}
	}
}
