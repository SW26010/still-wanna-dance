package console

import (
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

// Caller holds c.mu. Consent/startup enables automatic lightweight checks;
// status reads and SSE subscriptions remain inert.
func (c *Console) startAutomaticConnectivityLocked() error {
	if _, err := c.networkCalibrationLocked(); err != nil {
		return err
	}
	n := c.networkDiagnostics
	if n.connectivityAuto {
		return nil
	}
	n.connectivityAuto = true
	if !n.connectivity.Running {
		c.scheduleConnectivityLocked(0)
		c.publishNetworkConnectivityLocked()
	}
	return nil
}

// Completion of either connectivity trigger sets the next deadline.
func (c *Console) scheduleConnectivityLocked(delay time.Duration) {
	c.scheduleConnectivityAtLocked(time.Now().Add(delay))
}

func (c *Console) scheduleConnectivityAtLocked(due time.Time) {
	n := c.networkDiagnostics
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	n.connectivity.NextCheck = due
	n.connectivityTimer = time.AfterFunc(max(0, time.Until(due)), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closing || n.connectivity.Closed || n.connectivity.Running || !n.connectivity.NextCheck.Equal(due) {
			return
		}
		var operation upstreamcheck.Operation
		if n.connectivityRetry > 0 {
			operation = upstreamcheck.Catalog
		}
		c.startConnectivityLocked(operation, false)
	})
}
