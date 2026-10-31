package console

import (
	"errors"
	"net/http"

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
		c.networkDiagnostics.calibration = upstreamcheck.NewCalibration(c.client, c.settings.UpstreamMode)
	}
	return c.networkDiagnostics.calibration, nil
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
	if c.networkDiagnostics.connectivity.Running {
		return errors.New("连通性检测正在运行，请待其结束后开始下载速度测试")
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
