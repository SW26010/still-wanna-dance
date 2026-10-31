package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/upstreamcheck"
)

// Caller holds c.mu. Reading diagnostic state never starts a network request.
func (c *Console) networkCalibrationLocked() (*upstreamcheck.Calibration, error) {
	if c.closing {
		return nil, errors.New("程序正在退出")
	}
	if !c.terms.valid() {
		return nil, errors.New("请先阅读并同意使用条款")
	}
	if c.networkDiagnostics == nil {
		c.networkDiagnostics = &networkDiagnostics{}
	}
	if c.networkDiagnostics.calibration == nil {
		c.networkDiagnostics.calibration = upstreamcheck.NewCalibration(c.client, c.settings.UpstreamMode, upstreamcheck.CalibrationOptions{
			Candidates: c.calibrationCandidates, SelectSong: c.metadataLocked().SelectSongExcept,
			Playback: c.connectivityAccess().Playback, OnPlaybackURL: c.syncPlaybackURL,
			ResourceTransports: c.resourceTransports, OnFailedURL: c.rejectPlaybackURL,
		})
	}
	return c.networkDiagnostics.calibration, nil
}

// The application keeps this Store open until diagnostics stop. Query it only
// during preparation; subscribing to results must not read storage or network.
func (c *Console) calibrationCandidates(ctx context.Context) upstreamcheck.CalibrationCandidates {
	// The shared selector handles priority and traversal when each request starts.
	// Known songs remain a fallback, including complete but expired URLs.
	var candidates upstreamcheck.CalibrationCandidates
	store, err := c.businessStore()
	if err == nil {
		var known []int64
		known, err = store.KnownSongIDs(ctx)
		candidates.SongIDs = append(candidates.SongIDs, known...)
	}
	if err == nil {
		candidates.URLs, err = store.RecentSongURLs(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("network_calibration_local_songs_failed", "error", applog.SafeError(err))
		}
	}
	return candidates
}

func (c *Console) controlNetworkCalibration(cancel bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	test, err := c.networkCalibrationLocked()
	if err != nil {
		return err
	}
	if cancel {
		test.Cancel()
		return nil
	}
	return test.Start()
}

func (c *Console) serveNetworkCalibrationEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	c.mu.Lock()
	test, err := c.networkCalibrationLocked()
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	serveDiagnosticEvents(w, r, "calibration", func() diagnosticEventSnapshot {
		state, changed := test.Snapshot()
		return diagnosticEventSnapshot{state: state, revision: state.Revision, closed: state.Closed, changed: changed}
	})
}
