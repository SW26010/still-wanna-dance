package console

import (
	"context"
	"errors"
	"net/http"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamnet"
)

// Caller holds c.mu. Console owns admission and assembles the active network
// and business policies; diagnostics owns all checks and their lifecycle.
func (c *Console) networkDiagnosticsLocked() (*networkDiagnostics, error) {
	if c.closing {
		return nil, diagnosticsClosed()
	}
	if !c.terms.valid() {
		return nil, rejectRequest(errors.New("请先阅读并同意使用条款"))
	}
	if c.networkDiagnostics == nil {
		s, metadata := c.settings, c.metadataLocked()
		options := networkDiagnosticsOptions{
			Client: c.client, Upstream: s.UpstreamMode, Access: c.connectivityAccess(),
			Business: upstreamcheck.ConnectivityBusiness{
				OnCatalog: c.syncCatalogs, RememberSample: metadata.RememberSample, SelectSong: metadata.SelectSong,
				OnPlaybackURL: c.syncPlaybackURL, CachedResource: c.connectivityCachedResource,
			},
			CatalogReady: c.catalogReady, CachedSong: c.connectivityCachedSong,
			ReportFault: c.reportFault, ReportRetryFault: c.reportRetryFault,
			Calibration: upstreamcheck.CalibrationOptions{
				Candidates: c.calibrationCandidates, SelectSong: metadata.SelectSongExcept,
				Playback: c.connectivityAccess().Playback, OnPlaybackURL: c.syncPlaybackURL,
				ResourceTransports: c.resourceTransports,
				OnFinish: func(result upstreamcheck.CalibrationStatus) {
					if result.Phase == "failed" {
						c.reportFault("task:calibration", errors.New("网络测速未完成"))
					} else if result.Phase == "completed" {
						c.reportFault("task:calibration", nil)
					}
				},
			},
		}
		if s.UpstreamMode == "proxy" && s.ProxyBackend == "external-socks5" {
			options.SOCKS5 = func(ctx context.Context) (bool, error) {
				if s.proxySecretError != "" {
					return false, errors.New(unavailableProxySecret)
				}
				return upstreamnet.CheckSOCKS5Service(ctx, s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password)
			}
		}
		c.networkDiagnostics = newNetworkDiagnostics(options)
	}
	return c.networkDiagnostics, nil
}

func (c *Console) controlNetworkConnectivity(cancel bool) error {
	return c.controlNetworkConnectivityOperation(cancel, "")
}

func (c *Console) controlNetworkConnectivityOperation(cancel bool, operation upstreamcheck.Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.networkDiagnosticsLocked()
	if err != nil {
		return err
	}
	return n.controlConnectivity(cancel, operation)
}

func (c *Console) serveNetworkConnectivityEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", 403)
		return
	}
	c.mu.Lock()
	n, err := c.networkDiagnosticsLocked()
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	serveDiagnosticEvents(w, r, "connectivity", func() diagnosticEventSnapshot {
		state, changed := n.connectivitySnapshot()
		return diagnosticEventSnapshot{state: state, revision: state.Revision, closed: state.Closed, changed: changed}
	})
}

func (c *Console) checkSOCKS5Service() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.UpstreamMode != "proxy" || c.settings.ProxyBackend != "external-socks5" {
		return rejectRequest(errors.New("当前上游未使用外部 SOCKS5"))
	}
	n, err := c.networkDiagnosticsLocked()
	if err != nil {
		return err
	}
	return n.checkSOCKS5()
}

func (c *Console) socks5ServiceSnapshotLocked() *socks5ServiceStatus {
	if c.settings.UpstreamMode != "proxy" || c.settings.ProxyBackend != "external-socks5" {
		return nil
	}
	var status socks5ServiceStatus
	if c.networkDiagnostics != nil {
		status = c.networkDiagnostics.socks5Snapshot()
	}
	return &status
}
