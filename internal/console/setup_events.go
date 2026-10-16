package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type setupEventSnapshot struct {
	state    any
	revision uint64
	closed   bool
	changed  <-chan struct{}
}

// Callers authorize the request and capture state with its notification channel.
func serveSetupEvents(w http.ResponseWriter, r *http.Request, event string, snapshot func() setupEventSnapshot) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for {
		s := snapshot()
		body, err := json.Marshal(s.state)
		if err != nil {
			return
		}
		// A slow/disconnected browser must never hold shutdown or measurement.
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", s.revision, event, body); err != nil {
			return
		}
		if err = controller.Flush(); err != nil || s.closed {
			return
		}
		_ = controller.SetWriteDeadline(time.Time{})
		select {
		case <-r.Context().Done():
			return
		case <-s.changed:
		}
	}
}
