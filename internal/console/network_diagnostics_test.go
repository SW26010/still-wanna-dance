package console

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestNetworkDiagnosticsRunAndCancelIndependently(t *testing.T) {
	for _, first := range []string{"connectivity", "calibration"} {
		for _, canceled := range []string{"connectivity", "calibration"} {
			t.Run(first+"_then_cancel_"+canceled, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c := automaticTestConsole(t)
					seedCalibrationSong(t, c)
					started := stubNetworkDiagnostics(t, c)
					controls := map[string]func(bool) error{"connectivity": c.controlNetworkConnectivity, "calibration": c.controlNetworkCalibration}
					second := "calibration"
					if first == second {
						second = "connectivity"
					}
					for _, kind := range []string{first, second} {
						if err := controls[kind](false); err != nil {
							t.Fatalf("%s could not start alongside %s: %v", kind, first, err)
						}
						<-started // Both must reach real, independently canceled requests.
					}
					synctest.Wait()
					speed, _ := c.networkDiagnostics.calibration.Snapshot()
					if !speed.Running || !connectivitySnapshot(c).Running {
						t.Fatal("starting one diagnostic stopped the other")
					}
					if err := controls[canceled](true); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					speed, _ = c.networkDiagnostics.calibration.Snapshot()
					if speed.Running != (canceled != "calibration") || connectivitySnapshot(c).Running != (canceled != "connectivity") {
						t.Fatal("canceling one diagnostic affected the other")
					}
					c.BeginShutdown()
					synctest.Wait()
					speed, _ = c.networkDiagnostics.calibration.Snapshot()
					if speed.Running || connectivitySnapshot(c).Running {
						t.Fatal("shutdown did not drain both diagnostics")
					}
				})
			})
		}
	}
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
	before := upstreamrequest.Default.Snapshot()
	c.mu.Lock()
	test, err := c.networkCalibrationLocked()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state, _ := test.Snapshot()
	if state.Upstream != "proxy" || state.Running || c.monitor != nil {
		t.Fatal(state)
	}
	if after := upstreamrequest.Default.Snapshot(); after.Revision != before.Revision || after.Candidates != before.Candidates {
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
	c.networkDiagnostics = &networkDiagnostics{}
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
