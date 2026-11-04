package console

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestNetworkDiagnosticsRunAndCancelIndependently(t *testing.T) {
	for _, first := range []string{"connectivity", "calibration"} {
		for _, canceled := range []string{"connectivity", "calibration"} {
			t.Run(first+"_then_cancel_"+canceled, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := make(chan struct{}, 4)
					probe := func(ctx context.Context) {
						started <- struct{}{}
						<-ctx.Done()
					}
					options := diagnosticTestOptions()
					options.Access.Catalog = func(ctx context.Context, _ string) (upstreamcheck.Result, int64, upstreamcheck.CatalogResponse) {
						probe(ctx)
						return upstreamcheck.Result{}, 0, upstreamcheck.CatalogResponse{}
					}
					options.Calibration.Candidates = func(context.Context) upstreamcheck.CalibrationCandidates {
						return upstreamcheck.CalibrationCandidates{SongIDs: []int64{1}}
					}
					options.Calibration.Playback = func(ctx context.Context, _ int64, _ playbacksource.Entry) (upstreamcheck.Result, *upstreamcheck.Sample, upstreamcheck.PlaybackURLResponse) {
						probe(ctx)
						return upstreamcheck.Result{}, nil, upstreamcheck.PlaybackURLResponse{}
					}
					n := newNetworkDiagnostics(options)
					defer n.close()
					controls := map[string]func(bool) error{
						"connectivity": func(cancel bool) error { return n.controlConnectivity(cancel, "") },
						"calibration":  n.controlCalibration,
					}
					second := "calibration"
					if first == second {
						second = "connectivity"
					}
					for _, kind := range []string{first, second} {
						if err := controls[kind](false); err != nil {
							t.Fatalf("%s could not start alongside %s: %v", kind, first, err)
						}
						<-started // Both must reach independently canceled probes.
					}
					synctest.Wait()
					speed, _ := n.calibrationSnapshot()
					connectivity, _ := n.connectivitySnapshot()
					if !speed.Running || !connectivity.Running {
						t.Fatal("starting one diagnostic stopped the other")
					}
					if err := controls[canceled](true); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					speed, _ = n.calibrationSnapshot()
					connectivity, _ = n.connectivitySnapshot()
					if speed.Running != (canceled != "calibration") || connectivity.Running != (canceled != "connectivity") {
						t.Fatal("canceling one diagnostic affected the other")
					}
					n.close()
					speed, _ = n.calibrationSnapshot()
					connectivity, _ = n.connectivitySnapshot()
					if speed.Running || connectivity.Running || !speed.Closed || !connectivity.Closed {
						t.Fatal("shutdown did not drain both diagnostics")
					}
				})
			})
		}
	}
}

// Pure adapters keep lifecycle tests independent of Console, storage and terms.
// Protocol and business synchronization remain covered by integration tests.
func diagnosticTestOptions() networkDiagnosticsOptions {
	return networkDiagnosticsOptions{
		Client: &http.Client{}, Upstream: "proxy",
		CatalogReady: func(context.Context) bool { return true },
		CachedSong:   func(context.Context, int64) int64 { return 0 },
		Access: upstreamcheck.ConnectivityAccess{
			Catalog: func(_ context.Context, route string) (upstreamcheck.Result, int64, upstreamcheck.CatalogResponse) {
				return upstreamcheck.Result{Operation: upstreamcheck.Catalog, Route: route, State: "available"}, 1,
					upstreamcheck.CatalogResponse{Body: []byte("fixture")}
			},
		},
		Business: upstreamcheck.ConnectivityBusiness{
			OnCatalog:  func(context.Context, []upstreamcheck.CatalogResponse) (bool, error) { return true, nil },
			SelectSong: func(context.Context, int64) int64 { return 0 },
		},
	}
}

func TestNetworkDiagnosticsScheduleAndClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		options := diagnosticTestOptions()
		ready := false
		var n *networkDiagnostics
		options.CatalogReady = func(context.Context) bool {
			_, _ = n.connectivitySnapshot() // Business callbacks may read status.
			return ready
		}
		n = newNetworkDiagnostics(options)
		defer n.close()
		initial, changed := n.connectivitySnapshot()
		speed, _ := n.calibrationSnapshot()
		synctest.Wait()
		if initial.Run != 0 || initial.Running || speed.Running || changed == nil {
			t.Fatal("construction or subscription started a check", initial, speed)
		}
		for range 2 {
			if err := n.startAutomatic(); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		for _, delay := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
			state, _ := n.connectivitySnapshot()
			if !state.Initializing || state.Running || state.NextCheck.Sub(state.Finished) != delay {
				t.Fatal("incorrect initialization retry", delay, state)
			}
			time.Sleep(delay - time.Nanosecond)
			synctest.Wait()
			before, _ := n.connectivitySnapshot()
			if before.Run != state.Run {
				t.Fatal("retry ran before its deadline")
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			after, _ := n.connectivitySnapshot()
			if after.Run != state.Run+1 || after.Operation != upstreamcheck.Catalog {
				t.Fatal("automatic starts duplicated work or retried other operations", after)
			}
		}
		ready = true
		state, _ := n.connectivitySnapshot()
		time.Sleep(time.Until(state.NextCheck))
		synctest.Wait()
		state, _ = n.connectivitySnapshot()
		if state.Initializing || state.NextCheck.Sub(state.Finished) != 5*time.Minute {
			t.Fatal("recovery did not restore the regular period", state)
		}
		n.close() // Standalone close must cancel the timer as well as active work.
		n.close()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		last, _ := n.connectivitySnapshot()
		if !last.Closed || !last.NextCheck.IsZero() || last.Run != state.Run {
			t.Fatal("close left automatic work alive", last)
		}
	})
}

func TestNetworkDiagnosticsManualDeadlineIsExact(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		options := diagnosticTestOptions()
		catalog := options.Access.Catalog
		options.Access.Catalog = func(ctx context.Context, route string) (upstreamcheck.Result, int64, upstreamcheck.CatalogResponse) {
			time.Sleep(time.Second)
			return catalog(ctx, route)
		}
		n := newNetworkDiagnostics(options)
		defer n.close()
		if err := n.startAutomatic(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		previous, _ := n.connectivitySnapshot()
		for i := range 3 {
			time.Sleep(time.Minute)
			if err := n.controlConnectivity(false, upstreamcheck.Catalog); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			state, _ := n.connectivitySnapshot()
			if state.Running || !state.NextCheck.Equal(previous.NextCheck) || state.Run != previous.Run+uint64(i)+1 {
				t.Fatal("single check shifted the automatic deadline", state, previous)
			}
		}
	})
}

func TestNetworkDiagnosticsShutdownSignalsBeforeCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		options := diagnosticTestOptions()
		started, release := make(chan struct{}, 2), make(chan struct{})
		probe := func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
			<-release
		}
		options.Access.Catalog = func(ctx context.Context, _ string) (upstreamcheck.Result, int64, upstreamcheck.CatalogResponse) {
			probe(ctx)
			return upstreamcheck.Result{}, 0, upstreamcheck.CatalogResponse{}
		}
		options.SOCKS5 = func(ctx context.Context) (bool, error) { probe(ctx); return true, nil }
		n := newNetworkDiagnostics(options)
		if err := n.startAutomatic(); err != nil {
			t.Fatal(err)
		}
		<-started
		<-started
		state, changed := n.connectivitySnapshot()
		_, speedChanged := n.calibrationSnapshot()
		n.shutdown()
		n.shutdown()
		for _, event := range []<-chan struct{}{changed, speedChanged} {
			select {
			case <-event:
			default:
				t.Fatal("shutdown did not wake subscribers before draining")
			}
		}
		closed := make(chan struct{})
		go func() { n.close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("close did not wait for probe cleanup")
		default:
		}
		service := n.socks5Snapshot()
		if !service.Closed || service.Checking || !service.NextCheck.IsZero() {
			t.Fatal("shutdown did not close SOCKS5 status", service)
		}
		for _, trigger := range []func() error{
			n.startAutomatic, n.checkSOCKS5,
			func() error { return n.controlConnectivity(false, "") },
			func() error { return n.controlCalibration(false) },
		} {
			if err := trigger(); err == nil {
				t.Fatal("closed diagnostics accepted new work")
			}
		}
		close(release)
		<-closed
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		last, _ := n.connectivitySnapshot()
		if !last.Closed || last.Running || !last.NextCheck.IsZero() || last.Run != state.Run || len(last.Results) != 0 {
			t.Fatal("shutdown published late results or restarted a check", last)
		}
	})
}

func TestNetworkDiagnosticsCloseWaitsForEarlierResultObserver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		options := diagnosticTestOptions()
		observed, release := make(chan struct{}, 2), make(chan struct{})
		var n *networkDiagnostics
		options.ReportFault = func(string, error) {
			state, _ := n.connectivitySnapshot()
			observed <- struct{}{}
			if state.Run == 1 {
				<-release
			}
		}
		n = newNetworkDiagnostics(options)
		for range 2 {
			if err := n.controlConnectivity(false, upstreamcheck.Catalog); err != nil {
				t.Fatal(err)
			}
			<-observed
		}
		closed := make(chan struct{})
		go func() { n.close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("close only waited for the latest round")
		default:
		}
		close(release)
		<-closed
	})
}

func TestDiagnosticsBorrowActiveProxyWithoutChannels(t *testing.T) {
	c := testConsole(t)
	c.settings.UpstreamMode = "proxy"
	c.settings.SOCKS5Address = "127.0.0.1:9"
	c.savedSettings.UpstreamMode = "direct" // saved configuration is intentionally different
	_, client, err := c.networkFor(c.settings)
	if err != nil {
		t.Fatal(err)
	}
	c.client.CloseIdleConnections()
	c.client = client
	if _, ok := client.Transport.(upstreamrequest.Candidates); ok {
		t.Fatal("proxy exposes direct candidate interface")
	}
	before := c.requestChannel.Snapshot()
	c.mu.Lock()
	n, err := c.networkDiagnosticsLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state, _ := n.calibrationSnapshot()
	if state.Upstream != "proxy" || state.Running || c.monitor != nil {
		t.Fatal(state)
	}
	if after := c.requestChannel.Snapshot(); after.Revision != before.Revision || after.Candidates != before.Candidates {
		t.Fatal("diagnostics published channels")
	}
	if testDNS(c).queries.Load() != 0 {
		t.Fatal("proxy diagnostic queried direct DNS")
	}
}

func stubNetworkDiagnostics(t *testing.T, c *Console) <-chan struct{} {
	t.Helper()
	started := make(chan struct{}, 16)
	c.client.CloseIdleConnections()
	c.client = &http.Client{Transport: catalogTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	c.networkDiagnostics = nil
	return started
}

func activateTestProxy(t *testing.T, c *Console, address, user, password string) {
	t.Helper()
	c.settings.UpstreamMode, c.settings.ProxyBackend = "proxy", "external-socks5"
	c.settings.SOCKS5Address, c.settings.SOCKS5Username, c.settings.SOCKS5Password = address, user, password
	c.client.CloseIdleConnections()
	dial, client, err := c.networkFor(c.settings)
	if err != nil {
		t.Fatal(err)
	}
	c.upstreamDial, c.client = dial, client
}

// Business can still use its active transport when every direct candidate API
// rejects access. This also checks cold requests do not discover proxy channels.
type noCandidatesTransport struct{ http.RoundTripper }

func (noCandidatesTransport) Candidates(context.Context, string) ([]upstreamrequest.Candidate, error) {
	panic("proxy discovered channels")
}
func (noCandidatesTransport) Current(string) []upstreamrequest.Candidate {
	panic("proxy read channels")
}
func (noCandidatesTransport) Changed() <-chan struct{} { panic("proxy subscribed to channels") }
