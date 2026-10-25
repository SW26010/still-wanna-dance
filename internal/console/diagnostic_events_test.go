package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type diagnosticEventWriter struct {
	*httptest.ResponseRecorder
	t          *testing.T
	deadline   time.Time
	writeError bool
	flushError bool
	afterFlush func()
	writes     int
	flushes    int
}

func (w *diagnosticEventWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *diagnosticEventWriter) Write(body []byte) (int, error) {
	w.writes++
	if remaining := time.Until(w.deadline); remaining <= 0 || remaining > 5*time.Second {
		w.t.Error("event write is not bounded by the five-second deadline")
	}
	if w.writeError {
		return 0, errors.New("disconnected writer")
	}
	return w.ResponseRecorder.Write(body)
}

func (w *diagnosticEventWriter) FlushError() error {
	w.flushes++
	if !w.deadline.After(time.Now()) {
		w.t.Error("event flush has no active deadline")
	}
	if w.flushError {
		return errors.New("disconnected flush")
	}
	w.ResponseRecorder.Flush()
	if w.afterFlush != nil {
		w.afterFlush()
	}
	return nil
}

func TestDiagnosticEventsDisconnectAndRequestCancellation(t *testing.T) {
	for _, event := range []string{"calibration", "connectivity"} {
		for _, failure := range []string{"write", "flush", "cancel"} {
			t.Run(event+"/"+failure, func(t *testing.T) {
				c := testConsole(t)
				started := stubNetworkDiagnostics(t, c)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				r := httptest.NewRequest("GET", "http://"+c.address+"/api/network/"+event+"/events", nil).WithContext(ctx)
				w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t,
					writeError: failure == "write", flushError: failure == "flush"}
				if failure == "cancel" {
					w.afterFlush = cancel
				}
				c.ServeHTTP(w, r)
				if ctx.Err() == context.DeadlineExceeded {
					t.Fatal("event stream waited for timeout after disconnect")
				}
				if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("X-Accel-Buffering") != "no" {
					t.Fatal("event headers changed")
				}
				wantFlushes := 1
				if failure == "write" {
					wantFlushes = 0
				}
				if w.writes != 1 || w.flushes != wantFlushes {
					t.Fatalf("continued after disconnect: writes=%d flushes=%d", w.writes, w.flushes)
				}
				if failure == "cancel" && !w.deadline.IsZero() {
					t.Fatal("idle event stream retained its write deadline")
				}
				if failure != "write" {
					lines := strings.Split(w.Body.String(), "\n")
					if len(lines) != 5 || lines[1] != "event: "+event || lines[3] != "" || lines[4] != "" {
						t.Fatalf("invalid SSE frame: %q", w.Body.String())
					}
					var state struct {
						Revision uint64 `json:"revision"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &state); err != nil {
						t.Fatal(err)
					}
					if lines[0] != "id: "+strconv.FormatUint(state.Revision, 10) {
						t.Fatal("event ID differs from its snapshot revision")
					}
				}
				select {
				case <-started:
					t.Fatal("subscription started a network measurement")
				default:
				}
			})
		}
	}
}
