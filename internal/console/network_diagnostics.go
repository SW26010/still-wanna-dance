package console

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

// Diagnostics use the application's active network configuration and own only
// their checks; business clients and connections remain application-owned.
type networkDiagnostics struct {
	mu                  sync.Mutex
	tasks               sync.WaitGroup
	options             networkDiagnosticsOptions
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

// Options borrow the active access and business policies. Construction and
// snapshots are inert; only explicit triggers start checks. Callbacks run without mu.
type networkDiagnosticsOptions struct {
	Client           *http.Client
	Upstream         string
	Access           upstreamcheck.ConnectivityAccess
	Business         upstreamcheck.ConnectivityBusiness
	CatalogReady     func(context.Context) bool
	CachedSong       func(context.Context, int64) int64
	SOCKS5           func(context.Context) (bool, error)
	Calibration      upstreamcheck.CalibrationOptions
	ReportFault      func(string, error)
	ReportRetryFault func(string, error)
}

func newNetworkDiagnostics(options networkDiagnosticsOptions) *networkDiagnostics {
	n := &networkDiagnostics{options: options, connectivityChanged: make(chan struct{})}
	n.options.Business.CachedSong = n.cachedSong
	n.calibration = upstreamcheck.NewCalibration(options.Client, options.Upstream, options.Calibration)
	return n
}

// shutdown closes subscriptions and cancels work without waiting, so the owner
// can drain HTTP requests. close also works without a preceding shutdown.
func (n *networkDiagnostics) shutdown() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.connectivity.Closed {
		return
	}
	if n.connectivityTimer != nil {
		n.connectivityTimer.Stop()
	}
	n.connectivity.Closed = true
	n.connectivity.NextCheck = time.Time{}
	if n.connectivityCancel != nil {
		n.connectivityCancel()
	}
	n.socks5.Closed, n.socks5.Checking = true, false
	n.socks5.NextCheck = time.Time{}
	if n.socks5Cancel != nil {
		n.socks5Cancel()
	}
	n.publishConnectivityLocked()
	n.calibration.Shutdown()
}

func (n *networkDiagnostics) close() {
	if n == nil {
		return
	}
	n.shutdown()
	n.calibration.Close()
	n.tasks.Wait()
}

func (n *networkDiagnostics) controlCalibration(cancel bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.connectivity.Closed {
		return diagnosticsClosed()
	}
	if cancel {
		n.calibration.Cancel()
		return nil
	}
	return n.calibration.Start()
}

func (n *networkDiagnostics) calibrationSnapshot() (upstreamcheck.CalibrationStatus, <-chan struct{}) {
	return n.calibration.Snapshot()
}

func (n *networkDiagnostics) connectivitySnapshot() (networkConnectivityStatus, <-chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	state := n.connectivity
	state.Results = slices.Clone(state.Results)
	if state.LastResult != nil {
		last := *state.LastResult
		last.Results = slices.Clone(last.Results)
		state.LastResult = &last
	}
	return state, n.connectivityChanged
}

func (n *networkDiagnostics) socks5Snapshot() socks5ServiceStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.socks5
}

func (n *networkDiagnostics) cachedSong(ctx context.Context) int64 {
	n.mu.Lock()
	previous := n.connectivitySample
	n.mu.Unlock()
	id := n.options.CachedSong(ctx, previous)
	if id > 0 {
		n.mu.Lock()
		if ctx.Err() == nil {
			n.connectivitySample = id
		}
		n.mu.Unlock()
	}
	return id
}

func diagnosticsClosed() error {
	return rejectRequest(errors.New("程序正在退出"))
}
