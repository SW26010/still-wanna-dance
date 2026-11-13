package console

import (
	"context"
	"log/slog"
	"net/http"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/upstreamcheck"
)

// The application keeps this Store open until diagnostics stop. Query it only
// during preparation; subscribing to results must not read storage or network.
func (c *Console) calibrationCandidates(ctx context.Context) upstreamcheck.CalibrationCandidates {
	// The shared selector handles priority and traversal when each request starts.
	// Only incomplete current chains need resolution after saved media samples.
	var candidates upstreamcheck.CalibrationCandidates
	store, err := c.businessStore()
	if err == nil {
		var known []int64
		known, err = store.SongMetadataCandidates(ctx)
		candidates.SongIDs = append(candidates.SongIDs, known...)
	}
	if err == nil {
		candidates.URLs, err = store.AvailableMediaURLs(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("网络测速本地候选读取失败", "event_name", "network_calibration_local_songs_failed", "error", applog.SafeError(err))
		}
	}
	return candidates
}

func (c *Console) controlNetworkCalibration(cancel bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.networkDiagnosticsLocked()
	if err != nil {
		return err
	}
	return n.controlCalibration(cancel)
}

func (c *Console) serveNetworkCalibrationEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	c.mu.Lock()
	n, err := c.networkDiagnosticsLocked()
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	serveDiagnosticEvents(w, r, "calibration", func() diagnosticEventSnapshot {
		state, changed := n.calibrationSnapshot()
		return diagnosticEventSnapshot{state: state, revision: state.Revision, closed: state.Closed, changed: changed}
	})
}
