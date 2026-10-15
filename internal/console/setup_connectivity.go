package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type setupConnectivityStatus struct {
	LastValid map[string][]upstreamstate.ConnectivityResult `json:"lastValid"`
	Revision  uint64                                        `json:"revision"`
	Run       uint64                                        `json:"run"`
	Upstream  string                                        `json:"upstream"`
	Running   bool                                          `json:"running"`
	Closed    bool                                          `json:"closed"`
	Phase     string                                        `json:"phase"`
	Results   []upstreamstate.ConnectivityResult            `json:"results"`
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
	channel := n.channel
	release := func() {}
	if input.Upstream == "proxy" {
		_, client, err := c.networkFor(settings)
		if err != nil {
			return err
		}
		channel = upstreamrequest.NewChannel()
		revision := channel.Publish(client.Transport)
		release = func() {
			channel.Release(revision)
			client.CloseIdleConnections()
			if t, ok := client.Transport.(interface{ Retire() }); ok {
				t.Retire()
			}
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	n.connectivityCancel = stop
	n.connectivity = setupConnectivityStatus{LastValid: n.connectivity.LastValid, Revision: n.connectivity.Revision, Run: n.connectivity.Run + 1, Upstream: input.Upstream, Running: true, Phase: "running"}
	c.publishSetupConnectivityLocked()
	done := make(chan struct{})
	n.connectivityDone = done
	go func() {
		defer close(done)
		upstreamstate.CheckConnectivity(ctx, channel, input.Upstream, func(row upstreamstate.ConnectivityResult) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			rows := slices.Clone(n.connectivity.Results)
			found := false
			for i := range rows {
				if rows[i].Operation == row.Operation {
					rows[i], found = row, true
					break
				}
			}
			if !found {
				rows = append(rows, row)
			}
			n.connectivity.Results = rows
			c.publishSetupConnectivityLocked()
		})
		release()
		c.mu.Lock()
		n.connectivity.Running, n.connectivity.Phase = false, "completed"
		if ctx.Err() != nil {
			n.connectivity.Phase, n.connectivity.Results = "canceled", nil
		}
		if n.connectivity.Phase == "completed" && len(n.connectivity.Results) == 3 {
			saved := make(map[string][]upstreamstate.ConnectivityResult, len(n.connectivity.LastValid)+1)
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for {
		c.mu.Lock()
		state, changed := c.setupNetwork.connectivity, c.setupNetwork.connectivityChanged
		c.mu.Unlock()
		body, err := json.Marshal(state)
		if err != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "id: %d\nevent: connectivity\ndata: %s\n\n", state.Revision, body); err != nil {
			return
		}
		if err = controller.Flush(); err != nil || state.Closed {
			return
		}
		_ = controller.SetWriteDeadline(time.Time{})
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
	}
}
