package console

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"still-wanna-dance/internal/upstreamstate"
)

type setupConnectivityStatus struct {
	LastValid map[string][]setupConnectivityResult `json:"lastValid"`
	Revision  uint64                               `json:"revision"`
	Run       uint64                               `json:"run"`
	Upstream  string                               `json:"upstream"`
	Running   bool                                 `json:"running"`
	Closed    bool                                 `json:"closed"`
	Phase     string                               `json:"phase"`
	Results   []setupConnectivityResult            `json:"results"`
}

// The wizard presents one summary per operation; Monitor owns all probes.
// LatencyMS is the lowest estimated first-byte latency among available results,
// including Monitor's recent successful samples, not this run's elapsed time.
type setupConnectivityResult struct {
	Operation upstreamstate.Operation `json:"operation"`
	State     string                  `json:"state"`
	Reason    string                  `json:"reason,omitempty"`
	LatencyMS *float64                `json:"latencyMS"`
}

var connectivityOperations = []upstreamstate.Operation{upstreamstate.Catalog, upstreamstate.PlaybackURL, upstreamstate.Resource}

func summarizeConnectivity(op upstreamstate.Operation, results []upstreamstate.Result) setupConnectivityResult {
	row := setupConnectivityResult{Operation: op, State: "failed", Reason: "no_channel"}
	for _, r := range results {
		if r.Operation != op {
			continue
		}
		if r.State == "available" {
			if row.State != "available" || (r.EstimatedLatencyMS != nil && (row.LatencyMS == nil || *r.EstimatedLatencyMS < *row.LatencyMS)) {
				row.State, row.Reason, row.LatencyMS = "available", "", r.EstimatedLatencyMS
			}
		} else if row.State != "available" && r.State != "unknown" {
			row.Reason = r.Reason
			if row.Reason == "" {
				row.Reason = r.State
			}
		}
	}
	return row
}

// c.mu protects both the snapshot and its notification channel.
func (c *Console) publishSetupConnectivityLocked() {
	n := c.setupNetwork
	n.connectivity.Revision++
	if n.connectivityChanged != nil {
		close(n.connectivityChanged)
	}
	n.connectivityChanged = make(chan struct{})
}

func (c *Console) updateSetupConnectivity(ctx context.Context, progress upstreamstate.CheckProgress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.setupNetwork
	if n == nil || !n.connectivity.Running || n.connectivity.Closed || ctx.Err() != nil || n.connectivity.Phase == "canceling" {
		return
	}
	complete := progress.Operation == ""
	if !complete {
		n.connectivityPhase = slices.Index(connectivityOperations, progress.Operation)
	}
	// Only routes observed in this run contribute to its connectivity status.
	// Their latency estimates still include Monitor's recent successful samples.
	results := make([]upstreamstate.Result, 0, len(progress.Results))
	for _, result := range progress.Results {
		if !result.ObservedAt.Before(progress.Started) {
			results = append(results, result)
		}
	}
	var rows []setupConnectivityResult
	for i, op := range connectivityOperations {
		if i > n.connectivityPhase {
			if complete {
				rows = append(rows, setupConnectivityResult{Operation: op, State: "skipped", Reason: "no_sample"})
			}
			continue
		}
		row := summarizeConnectivity(op, results)
		if i == n.connectivityPhase && !complete {
			row = setupConnectivityResult{Operation: op, State: "checking"}
		}
		rows = append(rows, row)
	}
	n.connectivity.Results = rows
	c.publishSetupConnectivityLocked()
}

func (c *Console) controlSetupConnectivity(cancel bool, input setupCalibrationRequest) error {
	if input.Upstream == "" {
		input.Upstream = "direct"
	}
	if input.Upstream != "direct" && input.Upstream != "proxy" {
		return errors.New("未知检测上游")
	}
	settings, err := c.setupDiagnosticProxy(cancel, input)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	test, err := c.setupCalibrationLocked()
	if err != nil {
		return err
	}
	n := c.setupNetwork
	if cancel {
		if n.connectivity.Running {
			n.connectivity.Phase, n.connectivity.Results = "canceling", nil
			n.connectivityCancel()
			c.publishSetupConnectivityLocked()
		}
		return nil
	}
	if s, _ := test.Snapshot(); s.Running || n.running {
		return errors.New("请先停止当前检测")
	}
	if n.connectivity.Running {
		return errors.New("连通性检测正在运行")
	}
	monitor, err := c.setupConnectivityMonitorLocked(input.Upstream, settings)
	if err != nil {
		return err
	}
	client := n.client
	if input.Upstream == "proxy" {
		client = n.proxy.client
	}
	ctx, stop := context.WithCancel(context.Background())
	n.connectivityPhase = 0
	n.connectivityCancel = stop
	n.connectivity = setupConnectivityStatus{LastValid: n.connectivity.LastValid, Revision: n.connectivity.Revision, Run: n.connectivity.Run + 1, Upstream: input.Upstream, Running: true, Phase: "running"}
	c.publishSetupConnectivityLocked()
	done := make(chan struct{})
	n.connectivityDone = done
	go func() {
		defer close(done)
		// The reserved batch drains on cancellation while the monitor keeps
		// its song traversal for the next manual run on this network scope.
		err := monitor.CheckSelected(ctx, upstreamstate.CheckConnectivity)
		// http.Transport can keep an unused dial/handshake after the request
		// returns. Drain those connections without retiring this reusable pool.
		client.CloseIdleConnections()
		c.mu.Lock()
		n.connectivity.Running, n.connectivity.Phase = false, "completed"
		if err != nil {
			n.connectivity.Phase = "failed"
		}
		if ctx.Err() != nil {
			n.connectivity.Phase, n.connectivity.Results = "canceled", nil
		}
		if n.connectivity.Phase == "completed" && len(n.connectivity.Results) == 3 {
			saved := make(map[string][]setupConnectivityResult, len(n.connectivity.LastValid)+1)
			for key, rows := range n.connectivity.LastValid {
				saved[key] = rows
			}
			saved[input.Upstream] = slices.Clone(n.connectivity.Results)
			n.connectivity.LastValid = saved
		}
		stop()
		c.publishSetupConnectivityLocked()
		c.mu.Unlock()
	}()
	return nil
}

func (c *Console) serveSetupConnectivityEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", 403)
		return
	}
	c.mu.Lock()
	_, err := c.setupCalibrationLocked()
	if err == nil && c.setupNetwork.connectivityChanged == nil {
		c.setupNetwork.connectivityChanged = make(chan struct{})
	}
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	serveSetupEvents(w, r, "connectivity", func() setupEventSnapshot {
		c.mu.Lock()
		state, changed := c.setupNetwork.connectivity, c.setupNetwork.connectivityChanged
		c.mu.Unlock()
		return setupEventSnapshot{state: state, revision: state.Revision, closed: state.Closed, changed: changed}
	})
}
