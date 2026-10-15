package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type setupCalibrationRequest struct {
	Upstream string     `json:"upstream"`
	Proxy    *setupPlan `json:"proxy,omitempty"`
}

// Caller holds c.mu. Creating/subscribing never starts network measurement.
func (c *Console) setupCalibrationLocked() (*upstreamstate.Calibration, error) {
	if c.closing {
		return nil, errors.New("控制台正在退出")
	}
	if !c.terms.valid() {
		return nil, errors.New("请先阅读并同意使用条款")
	}
	if c.setupNetwork == nil {
		n, err := c.newSetupNetwork()
		if err != nil {
			return nil, err
		}
		c.setupNetwork = n
	}
	if c.setupNetwork.calibration == nil {
		c.setupNetwork.calibration = upstreamstate.NewCalibration(c.setupNetwork.channel)
	}
	return c.setupNetwork.calibration, nil
}

func (c *Console) controlSetupCalibration(cancel bool, input setupCalibrationRequest) error {
	if input.Upstream == "" {
		input.Upstream = "direct"
	}
	if input.Upstream != "direct" && input.Upstream != "proxy" {
		return errors.New("未知测速上游")
	}
	proxySettings, err := c.setupDiagnosticProxy(cancel, input)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	test, err := c.setupCalibrationLocked()
	if err != nil {
		return err
	}
	if cancel {
		test.Cancel()
		return nil
	}
	if c.setupNetwork.running || c.setupNetwork.connectivity.Running {
		return errors.New("连通性检测正在运行，请待其结束后开始 CF 并发测速")
	}
	if state, _ := test.Snapshot(); state.Running {
		if state.Upstream != input.Upstream {
			return errors.New("请先停止当前测速，再切换上游")
		}
		return nil
	}
	if input.Upstream == "direct" {
		return test.Start()
	}
	// Construct only on manual start; use the wizard draft without saving it.
	_, client, err := c.networkFor(proxySettings)
	if err != nil {
		return err
	}
	channel := upstreamrequest.NewChannel()
	revision := channel.Publish(client.Transport)
	return test.StartWithUpstream(channel, "proxy", func() {
		channel.Release(revision)
		client.CloseIdleConnections()
		if transport, ok := client.Transport.(interface{ Retire() }); ok {
			transport.Retire()
		}
	})
}

func (c *Console) serveSetupCalibrationEvents(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	c.mu.Lock()
	test, err := c.setupCalibrationLocked()
	c.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for {
		state, changed := test.Snapshot()
		body, err := json.Marshal(state)
		if err != nil {
			return
		}
		// A slow/disconnected browser must never hold shutdown or measurement.
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "id: %d\nevent: calibration\ndata: %s\n\n", state.Revision, body); err != nil {
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

// Both checks validate the same unsaved SOCKS5 draft.
func (c *Console) setupDiagnosticProxy(cancel bool, input setupCalibrationRequest) (Settings, error) {
	var proxySettings Settings
	if !cancel && input.Upstream == "proxy" {
		if input.Proxy == nil {
			return Settings{}, errors.New("请先在下方配置 SOCKS5 代理")
		}
		plan := *input.Proxy
		if plan.ProxyBackend != "" && plan.ProxyBackend != "external-socks5" {
			return Settings{}, errors.New("测速暂仅支持外部 SOCKS5 代理，请在下方选择外部 SOCKS5 后端")
		}
		plan.Page, plan.UpstreamMode = "proxy", "proxy"
		plan.ProxyImportID = ""
		var err error
		proxySettings, err = c.validateSetupPlan(plan)
		if err != nil {
			return Settings{}, err
		}
		if proxySettings.proxySecretError != "" {
			return Settings{}, errors.New(unavailableProxySecret)
		}
	}
	return proxySettings, nil
}
