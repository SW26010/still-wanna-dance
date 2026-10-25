package console

import (
	"errors"
	"net/http"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type networkCalibrationRequest struct {
	Upstream string                `json:"upstream"`
	Proxy    *diagnosticProxyDraft `json:"proxy,omitempty"`
}

// Only connection fields participate in a manual diagnostic. No settings are saved.
type diagnosticProxyDraft struct {
	ProxyBackend  string  `json:"proxyBackend"`
	SOCKS5Address string  `json:"socks5Address"`
	SOCKS5User    string  `json:"socks5Username"`
	Password      *string `json:"socks5Password"`
}

// Caller holds c.mu. Creating/subscribing never starts network measurement.
func (c *Console) networkCalibrationLocked() (*upstreamstate.Calibration, error) {
	if c.closing {
		return nil, errors.New("程序正在退出")
	}
	if !c.terms.valid() {
		return nil, errors.New("请先阅读并同意使用条款")
	}
	if c.networkDiagnostics == nil {
		n, err := c.newNetworkDiagnostics()
		if err != nil {
			return nil, err
		}
		c.networkDiagnostics = n
	}
	if c.networkDiagnostics.calibration == nil {
		c.networkDiagnostics.calibration = upstreamstate.NewCalibration(c.networkDiagnostics.channel)
	}
	return c.networkDiagnostics.calibration, nil
}

func (c *Console) controlNetworkCalibration(cancel bool, input networkCalibrationRequest) error {
	if input.Upstream == "" {
		input.Upstream = "direct"
	}
	if input.Upstream != "direct" && input.Upstream != "proxy" {
		return errors.New("未知测速上游")
	}
	proxySettings, err := c.diagnosticProxy(cancel, input)
	if err != nil {
		return err
	}
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
	if c.networkDiagnostics.connectivity.Running {
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
	// Construct only on manual start; use the settings draft without saving it.
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

// Both checks validate the same unsaved SOCKS5 draft.
func (c *Console) diagnosticProxy(cancel bool, input networkCalibrationRequest) (Settings, error) {
	var proxySettings Settings
	if !cancel && input.Upstream == "proxy" {
		if input.Proxy == nil {
			return Settings{}, errors.New("请先在网络设置中配置 SOCKS5 代理")
		}
		plan := *input.Proxy
		if plan.ProxyBackend != "" && plan.ProxyBackend != "external-socks5" {
			return Settings{}, errors.New("测速暂仅支持外部 SOCKS5 代理，请在网络设置中选择外部 SOCKS5 后端")
		}
		c.mu.Lock()
		proxySettings = c.savedSettings
		c.mu.Unlock()
		proxySettings.UpstreamMode, proxySettings.ProxyBackend = "proxy", "external-socks5"
		proxySettings.SOCKS5Address, proxySettings.SOCKS5Username = plan.SOCKS5Address, plan.SOCKS5User
		if plan.Password != nil {
			proxySettings.SOCKS5Password, proxySettings.proxySecret, proxySettings.proxySecretError = *plan.Password, "", ""
		}
		var err error
		proxySettings, err = c.resolveSettings(proxySettings)
		if err != nil {
			return Settings{}, err
		}
		if proxySettings.proxySecretError != "" {
			return Settings{}, errors.New(unavailableProxySecret)
		}
	}
	return proxySettings, nil
}
