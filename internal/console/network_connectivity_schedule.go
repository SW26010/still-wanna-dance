package console

import "time"

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

// Completion of either trigger sets the next deadline. An overdue timer waits
// on calibration events instead of canceling a manual speed test or polling.
func (c *Console) scheduleConnectivityLocked(delay time.Duration) {
	n := c.networkDiagnostics
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	due := time.Now().Add(delay)
	n.connectivity.NextCheck = due
	n.connectivityTimer = time.AfterFunc(delay, func() {
		for {
			c.mu.Lock()
			if c.closing || n.connectivity.Closed || n.connectivity.Running || !n.connectivity.NextCheck.Equal(due) {
				c.mu.Unlock()
				return
			}
			speed, changed := n.calibration.Snapshot()
			if speed.Running {
				c.mu.Unlock()
				<-changed
				continue
			}
			c.startConnectivityLocked(n.connectivityRetry > 0)
			c.mu.Unlock()
			return
		}
	})
}
