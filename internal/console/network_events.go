package console

import "net/http"

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
	stream := c.openRealtimeSocket(w, r)
	if stream == nil {
		return
	}
	defer stream.close()
	var connectivityRevision, calibrationRevision uint64
	first := true
	for {
		connectivity, connectivityChanged := n.connectivitySnapshot()
		calibration, calibrationChanged := n.calibrationSnapshot()
		if first || connectivity.Revision != connectivityRevision {
			if stream.write("connectivity", connectivity.Revision, connectivity) != nil {
				return
			}
			connectivityRevision = connectivity.Revision
		}
		if first || calibration.Revision != calibrationRevision {
			if stream.write("calibration", calibration.Revision, calibration) != nil {
				return
			}
			calibrationRevision = calibration.Revision
		}
		first = false
		if connectivity.Closed && calibration.Closed {
			return
		}
		select {
		case <-stream.ctx.Done():
			return
		case <-c.lifecycle.stop:
			return
		case <-connectivityChanged:
		case <-calibrationChanged:
		}
	}
}
