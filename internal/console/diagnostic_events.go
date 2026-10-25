package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type diagnosticEventSnapshot struct {
	state    any
	revision uint64
	closed   bool
	changed  <-chan struct{}
}

// Callers authorize the request and capture state with its notification channel.
func serveDiagnosticEvents(w http.ResponseWriter, r *http.Request, event string, snapshot func() diagnosticEventSnapshot) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for {
		s := snapshot()
		if err := writeServerEvent(w, controller, event, s.revision, s.state); err != nil || s.closed {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.changed:
		}
	}
}

// Event serialization and socket writes are always outside producer locks.
func writeServerEvent(w http.ResponseWriter, controller *http.ResponseController, event string, revision uint64, state any) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", revision, event, body); err != nil {
		return err
	}
	if err = controller.Flush(); err != nil {
		return err
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return nil
}
