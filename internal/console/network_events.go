package console

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Both diagnostics share one read-only connection per page. An upgraded socket
// does not retain an ordinary HTTP/1 connection slot, even across many windows.
func (c *Console) serveNetworkEvents(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "http://"+c.address {
		http.Error(w, "invalid network event origin", http.StatusForbidden)
		return
	}
	if !c.allowEventStream(w, r) {
		return
	}
	c.mu.Lock()
	n, err := c.networkDiagnosticsLocked()
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		// Read control frames and notice peer closure. Data messages are invalid
		// on this read-only stream; avoid CloseRead's self-wait in v1.8.12.
		defer cancel()
		_, _, _ = conn.Reader(ctx)
	}()
	write := func(event string, data any) error {
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return wsjson.Write(writeCtx, conn, struct {
			Event string `json:"event"`
			Data  any    `json:"data"`
		}{event, data})
	}
	var connectivityRevision, calibrationRevision uint64
	first := true
	for {
		connectivity, connectivityChanged := n.connectivitySnapshot()
		calibration, calibrationChanged := n.calibrationSnapshot()
		if first || connectivity.Revision != connectivityRevision {
			if write("connectivity", connectivity) != nil {
				return
			}
			connectivityRevision = connectivity.Revision
		}
		if first || calibration.Revision != calibrationRevision {
			if write("calibration", calibration) != nil {
				return
			}
			calibrationRevision = calibration.Revision
		}
		first = false
		if connectivity.Closed && calibration.Closed {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-c.lifecycle.stop:
			return
		case <-connectivityChanged:
		case <-calibrationChanged:
		}
	}
}
