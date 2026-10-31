package console

import (
	"context"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

// Diagnostics borrow the application's active client. They own only their runs.
type networkDiagnostics struct {
	connectivity        networkConnectivityStatus
	connectivityChanged chan struct{}
	connectivityCancel  context.CancelFunc
	connectivityDone    chan struct{}
	connectivityAuto    bool
	connectivityRetry   time.Duration
	connectivityTimer   *time.Timer
	calibration         *upstreamcheck.Calibration
}

func (n *networkDiagnostics) close() {
	if n == nil {
		return
	}
	if n.connectivityCancel != nil {
		n.connectivityCancel()
	}
	if n.calibration != nil {
		n.calibration.Close()
	}
	if n.connectivityDone != nil {
		<-n.connectivityDone
	}
}
