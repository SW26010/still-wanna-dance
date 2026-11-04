package console

import (
	"context"
	"errors"
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
	Operation    upstreamcheck.Operation     `json:"operation,omitempty"`
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
	Retained   bool                    `json:"retained,omitempty"`
	// Retain this round's completed resource observation while retrying it.
	retryPrevious *networkConnectivityResult
}

// Only completed current observations count: one usable route recovers the
// operation even if another route failed. Retried or retained rows stay quiet.
func connectivityFaults(rows []networkConnectivityResult, selected upstreamcheck.Operation) map[upstreamcheck.Operation]error {
	type outcome struct{ failed, available bool }
	observed := make(map[upstreamcheck.Operation]outcome)
	for _, row := range rows {
		if row.Retained || selected != "" && row.Operation != selected {
			continue
		}
		state := observed[row.Operation]
		switch row.State {
		case "available":
			state.available = true
		case "failed":
			state.failed = true
		default:
			continue
		}
		observed[row.Operation] = state
	}
	result := make(map[upstreamcheck.Operation]error, len(observed))
	for operation, state := range observed {
		if state.failed && !state.available {
			result[operation] = errors.New("连通性检测失败")
		} else {
			result[operation] = nil
		}
	}
	return result
}

// Keep real observations when a later run cannot attempt the same operation.
// The retained marker distinguishes history from the current run.
func retainUnobservedConnectivity(results, previous []networkConnectivityResult) []networkConnectivityResult {
	for _, operation := range []upstreamcheck.Operation{upstreamcheck.PlaybackURL, upstreamcheck.Resource} {
		observed := slices.ContainsFunc(results, func(row networkConnectivityResult) bool {
			return row.Operation == operation && !row.ObservedAt.IsZero()
		})
		if observed {
			continue
		}
		for _, row := range previous {
			if row.Operation == operation && !row.ObservedAt.IsZero() {
				row.Retained = true
				results = append(results, row)
			}
		}
	}
	return results
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
		if row.Operation == upstreamcheck.Resource && row.State == "checking" && !rows[i].ObservedAt.IsZero() {
			previous := rows[i]
			row.retryPrevious = &previous
		}
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
	return c.controlNetworkConnectivityOperation(cancel, "")
}

func (c *Console) controlNetworkConnectivityOperation(cancel bool, operation upstreamcheck.Operation) error {
	if operation != "" && operation != upstreamcheck.Catalog && operation != upstreamcheck.PlaybackURL && operation != upstreamcheck.Resource {
		return rejectRequest(errors.New("无效的连通性检测项目"))
	}
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
		if operation != "" || n.connectivity.Operation != "" {
			return rejectRequest(errors.New("连通性检测正在进行，请稍后重试"))
		}
		n.connectivityManual = true
		c.publishNetworkConnectivityLocked()
		return nil // Full checks join the existing full round.
	}
	c.startConnectivityLocked(operation, operation != "", true)
	return nil
}

// Caller holds c.mu. All triggers share progress, publication and cancellation.
func (c *Console) startConnectivityLocked(operation upstreamcheck.Operation, keepSchedule, manual bool) {
	n := c.networkDiagnostics
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	ctx, stop := context.WithCancel(context.Background())
	n.connectivityCancel = stop
	n.connectivityManual = manual
	previous := n.connectivity
	n.connectivity = networkConnectivityStatus{Finished: previous.Finished, LastResult: previous.LastResult, Revision: previous.Revision, Run: previous.Run + 1, Upstream: c.settings.UpstreamMode, Operation: operation, Running: true, Phase: "running", Initializing: previous.Initializing}
	if keepSchedule {
		n.connectivity.NextCheck = previous.NextCheck
	}
	if operation != "" && previous.LastResult != nil {
		for _, row := range previous.LastResult.Results {
			if !keepSchedule || row.Operation != operation {
				n.connectivity.Results = append(n.connectivity.Results, row)
			}
		}
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
			OnCatalog: c.syncCatalogs, RememberSample: metadata.RememberSample, SelectSong: metadata.SelectSong, OnPlaybackURL: c.syncPlaybackURL,
			CachedSong: c.connectivityCachedSong, CachedResource: c.connectivityCachedResource,
		}, operation, func(r upstreamcheck.Result) { c.connectivityRow(ctx, connectivityResult(r)) })
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
			results := slices.Clone(n.connectivity.Results)
			for i, row := range results {
				if row.State == "checking" && row.retryPrevious != nil {
					results[i] = *row.retryPrevious
				}
			}
			n.connectivity.Results = slices.DeleteFunc(results, func(row networkConnectivityResult) bool { return row.State == "checking" })
			if previous.LastResult != nil {
				n.connectivity.Results = retainUnobservedConnectivity(n.connectivity.Results, previous.LastResult.Results)
			}
			n.connectivity.LastResult = &networkConnectivityRecord{Finished: n.connectivity.Finished, Results: slices.Clone(n.connectivity.Results)}
			for checked, failure := range connectivityFaults(n.connectivity.Results, operation) {
				source := "network:connectivity:" + string(checked)
				if n.connectivityManual {
					c.reportFault(source, failure)
				} else {
					c.reportRetryFault(source, failure)
				}
			}
			if ready {
				n.connectivityRetry = 0
			} else if !keepSchedule {
				n.connectivityRetry = min(max(5*time.Second, 2*n.connectivityRetry), time.Minute)
				delay = n.connectivityRetry
			}
		}
		if n.connectivityAuto && !n.connectivity.Closed {
			if keepSchedule && !previous.NextCheck.IsZero() {
				c.scheduleConnectivityAtLocked(previous.NextCheck)
			} else {
				c.scheduleConnectivityLocked(delay)
			}
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
