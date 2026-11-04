package console

import (
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

// Consent/startup explicitly enables lightweight checks. Construction, status
// reads and subscriptions remain inert. Repeated starts join the same schedule.
func (n *networkDiagnostics) startAutomatic() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.connectivity.Closed {
		return diagnosticsClosed()
	}
	if n.connectivityAuto {
		return nil
	}
	n.connectivityAuto = true
	n.startSOCKS5Locked()
	if !n.connectivity.Running {
		n.scheduleConnectivityLocked(0)
		n.publishConnectivityLocked()
	}
	return nil
}

// Completion of either connectivity trigger sets the next deadline.
func (n *networkDiagnostics) scheduleConnectivityLocked(delay time.Duration) {
	n.scheduleConnectivityAtLocked(time.Now().Add(delay))
}

func (n *networkDiagnostics) scheduleConnectivityAtLocked(due time.Time) {
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	n.connectivity.NextCheck = due
	n.connectivityTimer = time.AfterFunc(max(0, time.Until(due)), func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.connectivity.Closed || n.connectivity.Running || !n.connectivity.NextCheck.Equal(due) {
			return
		}
		var operation upstreamcheck.Operation
		if n.connectivityRetry > 0 {
			operation = upstreamcheck.Catalog
		}
		n.startConnectivityLocked(operation, false, false)
	})
}
