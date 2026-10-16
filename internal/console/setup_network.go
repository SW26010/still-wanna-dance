package console

import (
	"context"
	"errors"
	"net/http"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type setupNetwork struct {
	connectivityPhase   int
	proxy               *setupMonitorScope
	connectivity        setupConnectivityStatus
	connectivityChanged chan struct{}
	connectivityCancel  context.CancelFunc
	connectivityDone    chan struct{}
	calibration         *upstreamstate.Calibration
	monitor             *upstreamstate.Monitor
	client              *http.Client
	channel             *upstreamrequest.Channel
	revision            uint64
	done                chan struct{}
	running             bool
	err                 string
}

// A draft proxy keeps its Monitor (including the song traversal) until the
// proxy identity changes or the application exits. It never starts a scheduler.
type setupMonitorScope struct {
	monitor  *upstreamstate.Monitor
	client   *http.Client
	channel  *upstreamrequest.Channel
	revision uint64
	identity string
}

func (s *setupMonitorScope) close() {
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

func (c *Console) newSetupMonitor(channel *upstreamrequest.Channel) (*upstreamstate.Monitor, error) {
	return c.newUpstreamMonitor(upstreamstate.Options{
		Channel: channel, Policy: upstreamstate.Policy{RequestTimeout: 10 * time.Second},
		OnProgress: c.updateSetupConnectivity,
	})
}

// Caller holds c.mu and has excluded every other setup network task.
func (c *Console) setupConnectivityMonitorLocked(upstream string, settings Settings) (*upstreamstate.Monitor, error) {
	n := c.setupNetwork
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
	revision := channel.Publish(client.Transport)
	monitor, err := c.newSetupMonitor(channel)
	if err != nil {
		channel.Release(revision)
		client.CloseIdleConnections()
		client.Transport.(interface{ Retire() }).Retire()
		return nil, err
	}
	// The old scope is idle: no callback can be waiting for c.mu here.
	n.proxy.close()
	n.proxy = &setupMonitorScope{monitor, client, channel, revision, identity}
	return monitor, nil
}

type setupNetworkStatus struct {
	Running bool                 `json:"running"`
	Error   string               `json:"error,omitempty"`
	Monitor upstreamstate.Status `json:"monitor"`
}

// Caller holds c.mu. Reading never starts a check or changes business routing.
func (c *Console) setupNetworkSnapshotLocked() setupNetworkStatus {
	if c.setupNetwork == nil {
		return setupNetworkStatus{}
	}
	n := c.setupNetwork
	return setupNetworkStatus{n.running, n.err, n.monitor.Snapshot()}
}

func (c *Console) newSetupNetwork() (*setupNetwork, error) {
	// Never publish the diagnostic transport to Default, swap c.client, or
	// change settings. A private channel also isolates discovered resource URLs.
	_, client, err := c.networkFor(Settings{UpstreamMode: "direct"})
	if err != nil {
		return nil, err
	}
	channel := upstreamrequest.NewChannel()
	revision := channel.Publish(client.Transport)
	monitor, err := c.newSetupMonitor(channel)
	if err != nil {
		client.Transport.(interface{ Retire() }).Retire()
		return nil, err
	}
	return &setupNetwork{monitor: monitor, client: client, channel: channel, revision: revision}, nil
}

func (c *Console) checkSetupNetwork(selected ...upstreamstate.CheckKind) error {
	var kind upstreamstate.CheckKind
	if len(selected) > 0 {
		kind = selected[0]
		if !upstreamstate.ValidCheckKind(kind) {
			return errors.New("未知检测项目")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if !c.terms.valid() {
		return errors.New("请先阅读并同意使用条款")
	}
	if c.setupNetwork == nil {
		var err error
		c.setupNetwork, err = c.newSetupNetwork()
		if err != nil {
			return err
		}
	}
	n := c.setupNetwork
	if n.connectivity.Running {
		return errors.New("请先停止连通性检测")
	}
	if n.calibration != nil {
		if state, _ := n.calibration.Snapshot(); state.Running {
			return errors.New("请先停止 CF 并发测速，再运行直连诊断")
		}
	}
	if n.running {
		return nil
	}
	n.running, n.err = true, ""
	done := make(chan struct{})
	n.done = done
	go func() {
		defer close(done)
		// An explicit complete Monitor check includes throughput and bypasses the
		// automatic cooldown. It lasts beyond a browser request or page navigation.
		var err error
		if kind == "" {
			err = n.monitor.Check(context.Background())
		} else {
			err = n.monitor.CheckSelected(context.Background(), kind)
		}
		c.mu.Lock()
		n.running = false
		if err != nil {
			n.err = err.Error()
		}
		c.mu.Unlock()
	}()
	return nil
}

func (n *setupNetwork) close() {
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
	if n.done != nil {
		<-n.done
	}
	n.client.CloseIdleConnections()
	if transport, ok := n.client.Transport.(interface{ Retire() }); ok {
		transport.Retire()
	}
}
