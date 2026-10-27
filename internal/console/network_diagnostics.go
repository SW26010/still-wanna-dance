package console

import (
	"context"
	"net/http"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type networkDiagnostics struct {
	connectivityPhase   int
	proxy               *diagnosticMonitorScope
	connectivity        networkConnectivityStatus
	connectivityChanged chan struct{}
	connectivityCancel  context.CancelFunc
	connectivityDone    chan struct{}
	calibration         *upstreamstate.Calibration
	monitor             *upstreamstate.Monitor
	client              *http.Client
	channel             *upstreamrequest.Channel
	revision            uint64
}

// A draft proxy keeps its Monitor (including the song traversal) until the
// proxy identity changes or the application exits. It never starts a scheduler.
type diagnosticMonitorScope struct {
	monitor  *upstreamstate.Monitor
	client   *http.Client
	channel  *upstreamrequest.Channel
	revision uint64
	identity string
}

func (s *diagnosticMonitorScope) close() {
	if s == nil {
		return
	}
	s.monitor.Close()
	s.channel.Release(s.revision)
	s.client.CloseIdleConnections()
	if transport, ok := s.client.Transport.(interface{ Retire() }); ok {
		transport.Retire()
	}
}

func (c *Console) newDiagnosticMonitor(channel *upstreamrequest.Channel) (*upstreamstate.Monitor, error) {
	return c.newUpstreamMonitor(upstreamstate.Options{
		Channel: channel, Policy: upstreamstate.Policy{RequestTimeout: 10 * time.Second},
		OnProgress: c.updateNetworkConnectivity,
	})
}

// Caller holds c.mu and has excluded every other diagnostic task.
func (c *Console) networkConnectivityMonitorLocked(upstream string, settings Settings) (*upstreamstate.Monitor, error) {
	n := c.networkDiagnostics
	if upstream == "direct" {
		return n.monitor, nil
	}
	identity, err := c.proxyIdentity(settings)
	if err != nil {
		return nil, err
	}
	if n.proxy != nil && identity != "" && n.proxy.identity == identity {
		return n.proxy.monitor, nil
	}
	_, client, err := c.networkFor(settings)
	if err != nil {
		return nil, err
	}
	channel := upstreamrequest.NewChannel()
	revision := channel.Publish(client.Transport.(upstreamrequest.Candidates))
	monitor, err := c.newDiagnosticMonitor(channel)
	if err != nil {
		channel.Release(revision)
		client.CloseIdleConnections()
		client.Transport.(interface{ Retire() }).Retire()
		return nil, err
	}
	// The old scope is idle: no callback can be waiting for c.mu here.
	n.proxy.close()
	n.proxy = &diagnosticMonitorScope{monitor, client, channel, revision, identity}
	return monitor, nil
}

func (c *Console) newNetworkDiagnostics() (*networkDiagnostics, error) {
	// Never publish the diagnostic transport to Default, swap c.client, or
	// change settings. A private channel also isolates discovered resource URLs.
	_, client, err := c.networkFor(Settings{UpstreamMode: "direct"})
	if err != nil {
		return nil, err
	}
	channel := upstreamrequest.NewChannel()
	revision := channel.Publish(client.Transport.(upstreamrequest.Candidates))
	monitor, err := c.newDiagnosticMonitor(channel)
	if err != nil {
		client.Transport.(interface{ Retire() }).Retire()
		return nil, err
	}
	return &networkDiagnostics{monitor: monitor, client: client, channel: channel, revision: revision}, nil
}

func (n *networkDiagnostics) close() {
	if n == nil {
		return
	}
	if n.calibration != nil {
		n.calibration.Close()
	}
	if n.connectivityDone != nil {
		<-n.connectivityDone
	}
	n.proxy.close()
	n.monitor.Close()
	n.client.CloseIdleConnections()
	if transport, ok := n.client.Transport.(interface{ Retire() }); ok {
		transport.Retire()
	}
}
