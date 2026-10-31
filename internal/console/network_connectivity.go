package console

import (
	"context"
	"net/http"
	"slices"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

type networkConnectivityRecord struct {
	Finished time.Time                   `json:"finished"`
	Results  []networkConnectivityResult `json:"results"`
}
type networkConnectivityStatus struct {
	LastResult   *networkConnectivityRecord  `json:"lastResult,omitempty"`
	Finished     time.Time                   `json:"finished"`
	NextCheck    time.Time                   `json:"nextCheck"`
	Initializing bool                        `json:"initializing"`
	Revision     uint64                      `json:"revision"`
	Run          uint64                      `json:"run"`
	Upstream     string                      `json:"upstream"`
	Running      bool                        `json:"running"`
	Closed       bool                        `json:"closed"`
	Phase        string                      `json:"phase"`
	Results      []networkConnectivityResult `json:"results"`
}
type networkConnectivityResult struct {
	Operation  upstreamcheck.Operation `json:"operation"`
	Entry      string                  `json:"entry"`
	State      string                  `json:"state"`
	Reason     string                  `json:"reason,omitempty"`
	Scheme     string                  `json:"scheme,omitempty"`
	LatencyMS  *float64                `json:"latencyMS"`
	ObservedAt time.Time               `json:"observedAt"`
}

func (c *Console) publishNetworkConnectivityLocked() {
	n := c.networkDiagnostics
	n.connectivity.Revision++
	if n.connectivityChanged != nil {
		close(n.connectivityChanged)
	}
	n.connectivityChanged = make(chan struct{})
}

func (c *Console) connectivityRow(ctx context.Context, row networkConnectivityResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.networkDiagnostics
	if ctx.Err() != nil || n.connectivity.Closed {
		return
	}
	rows := slices.Clone(n.connectivity.Results)
	i := slices.IndexFunc(rows, func(r networkConnectivityResult) bool { return r.Operation == row.Operation && r.Entry == row.Entry })
	if i < 0 {
		rows = append(rows, row)
	} else {
		rows[i] = row
	}
	n.connectivity.Results = rows
	c.publishNetworkConnectivityLocked()
}

func connectivityResult(r upstreamcheck.Result) networkConnectivityResult {
	row := networkConnectivityResult{Operation: r.Operation, Entry: r.Route, State: "failed", Reason: r.State, Scheme: r.Scheme}
	if r.State == "available" {
		row.State = "available"
		row.Reason = ""
	} else if r.State == "checking" || r.State == "skipped" {
		row.State = r.State
		row.Reason = ""
	}
	if r.State != "checking" && r.State != "skipped" {
		row.ObservedAt = r.ObservedAt
		if row.ObservedAt.IsZero() {
			row.ObservedAt = time.Now()
		}
	}
	if r.Responded {
		ms := float64(r.Latency) / float64(time.Millisecond)
		row.LatencyMS = &ms
	}
	return row
}

func (c *Console) controlNetworkConnectivity(cancel bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.networkCalibrationLocked()
	if err != nil {
		return err
	}
	n := c.networkDiagnostics
	if cancel {
		if n.connectivity.Running {
			n.connectivity.Phase = "canceling"
			n.connectivityCancel()
			c.publishNetworkConnectivityLocked()
		}
		return nil
	}
	if n.connectivity.Running {
		c.publishNetworkConnectivityLocked()
		return nil // Both triggers join the same round; never queue duplicate work.
	}
	c.startConnectivityLocked(false)
	return nil
}

// Caller holds c.mu. All triggers share progress, publication and cancellation.
func (c *Console) startConnectivityLocked(catalogOnly bool) {
	n := c.networkDiagnostics
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	ctx, stop := context.WithCancel(context.Background())
	n.connectivityCancel = stop
	previous := n.connectivity
	n.connectivity = networkConnectivityStatus{Finished: previous.Finished, LastResult: previous.LastResult, Revision: previous.Revision, Run: previous.Run + 1, Upstream: c.settings.UpstreamMode, Running: true, Phase: "running", Initializing: previous.Initializing}
	if catalogOnly && previous.LastResult != nil {
		n.connectivity.Results = slices.Clone(previous.LastResult.Results)
	}
	c.publishNetworkConnectivityLocked()
	done := make(chan struct{})
	n.connectivityDone = done
	client, metadata := c.client, c.metadataLocked()
	go func() {
		defer close(done)
		defer stop()
		ready := c.catalogReady(ctx)
		c.mu.Lock()
		n.connectivity.Initializing = !ready
		c.publishNetworkConnectivityLocked()
		c.mu.Unlock()
		upstreamcheck.CheckConnectivity(ctx, c.connectivityAccess(), upstreamcheck.ConnectivityBusiness{
			OnCatalog: c.syncCatalogs, SelectSong: metadata.SelectSong, OnPlaybackURL: c.syncPlaybackURL,
		}, catalogOnly, func(r upstreamcheck.Result) { c.connectivityRow(ctx, connectivityResult(r)) })
		if ctx.Err() == nil {
			ready = c.catalogReady(ctx)
		}
		// Cancel unused dials retained by net/http after request cancellation.
		client.CloseIdleConnections()
		c.mu.Lock()
		defer c.mu.Unlock()
		n.connectivity.Running = false
		delay := 5 * time.Minute
		if ctx.Err() != nil {
			n.connectivity.Phase = "canceled"
			n.connectivity.Results = nil
		} else {
			n.connectivity.Phase = "completed"
			n.connectivity.Finished = time.Now()
			n.connectivity.Initializing = !ready
			n.connectivity.LastResult = &networkConnectivityRecord{Finished: n.connectivity.Finished, Results: slices.Clone(n.connectivity.Results)}
			if ready {
				n.connectivityRetry = 0
			} else {
				n.connectivityRetry = min(max(5*time.Second, 2*n.connectivityRetry), time.Minute)
				delay = n.connectivityRetry
			}
		}
		if n.connectivityAuto && !n.connectivity.Closed {
			c.scheduleConnectivityLocked(delay)
		}
		c.publishNetworkConnectivityLocked()
	}()
}

func (c *Console) serveNetworkConnectivityEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", 403)
		return
	}
	c.mu.Lock()
	_, err := c.networkCalibrationLocked()
	if err == nil && c.networkDiagnostics.connectivityChanged == nil {
		c.networkDiagnostics.connectivityChanged = make(chan struct{})
	}
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	serveDiagnosticEvents(w, r, "connectivity", func() diagnosticEventSnapshot {
		c.mu.Lock()
		state, changed := c.networkDiagnostics.connectivity, c.networkDiagnostics.connectivityChanged
		c.mu.Unlock()
		return diagnosticEventSnapshot{state: state, revision: state.Revision, closed: state.Closed, changed: changed}
	})
}
