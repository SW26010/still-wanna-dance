package console

import (
	"context"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

// Diagnostics use the application's active network configuration and own only
// their checks; business clients and connections remain application-owned.
type networkDiagnostics struct {
	socks5              socks5ServiceStatus
	socks5Cancel        context.CancelFunc
	socks5Done          chan struct{}
	socks5Wake          chan struct{}
	connectivity        networkConnectivityStatus
	connectivityChanged chan struct{}
	connectivityCancel  context.CancelFunc
	connectivityDone    chan struct{}
	connectivityAuto    bool
	connectivityManual  bool
	connectivityRetry   time.Duration
	connectivityTimer   *time.Timer
	connectivitySample  int64
	calibration         *upstreamcheck.Calibration
}

func (n *networkDiagnostics) close() {
	if n == nil {
		return
	}
	if n.connectivityCancel != nil {
		n.connectivityCancel()
	}
	if n.socks5Cancel != nil {
		n.socks5Cancel()
	}
	if n.calibration != nil {
		n.calibration.Close()
	}
	if n.connectivityDone != nil {
		<-n.connectivityDone
	}
	if n.socks5Done != nil {
		<-n.socks5Done
	}
}
