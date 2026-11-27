package console

import (
	"context"
	"errors"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

// Direct Monitor responses may feed the independent business synchronization.
func (c *Console) newUpstreamMonitor(o upstreamstate.Options) (*upstreamstate.Monitor, error) {
	o.OnCatalog = func(ctx context.Context, responses []upstreamstate.CatalogResponse) {
		c.syncCatalogs(ctx, responses)
	}
	o.OnPlaybackURL = c.syncPlaybackURL
	o.Channel = c.requestChannel
	return upstreamstate.NewMonitor(o)
}

// StartUpstreamServices starts shared automatic checks and, for direct only, Monitor.
// A pending start is completed by acceptTerms; constructing/reading never probes.
func (c *Console) StartUpstreamServices() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamRequested = true
	return c.startUpstreamServicesLocked()
}
func (c *Console) startUpstreamServicesLocked() (resultErr error) {
	defer func() { c.reportFault("upstream:start", resultErr) }()
	if c.closing {
		return rejectRequest(errors.New("程序正在退出"))
	}
	if !c.upstreamRequested || !c.terms.valid() {
		return nil
	}
	n, err := c.networkDiagnosticsLocked()
	if err != nil {
		return err
	}
	if err := n.startAutomatic(); err != nil {
		return err
	}
	c.startResourceSamplerLocked()
	if c.settings.UpstreamMode != "direct" {
		return nil
	}
	m, err := c.ensureMonitorLocked()
	if err != nil {
		return err
	}
	return m.Start()
}

// A primary catalog read can use the shared monitor before the scheduler starts.
func (c *Console) directCatalogMonitor() (*upstreamstate.Monitor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.UpstreamMode != "direct" {
		return nil, nil
	}
	if c.closing {
		return nil, context.Canceled
	}
	if c.monitor == nil {
		if _, ok := c.client.Transport.(upstreamrequest.Candidates); !ok {
			return nil, nil
		}
	}
	return c.ensureMonitorLocked()
}

func (c *Console) ensureMonitorLocked() (*upstreamstate.Monitor, error) {
	if c.monitor == nil {
		m, err := c.newUpstreamMonitor(upstreamstate.Options{})
		if err != nil {
			return nil, err
		}
		c.monitor = m
	}
	return c.monitor, nil
}

func monitorSnapshot(m *upstreamstate.Monitor, manual bool) upstreamstate.Status {
	if m == nil {
		return upstreamstate.Status{Policy: upstreamstate.DefaultPolicy(), Results: []upstreamstate.Result{}}
	}
	s := m.Snapshot()
	s.Checking = s.Checking || manual
	s.Manual = s.Manual || manual
	return s
}
func (c *Console) requestMonitorCheck(selected ...upstreamstate.CheckKind) error {
	var kind upstreamstate.CheckKind
	if len(selected) > 0 {
		kind = selected[0]
	}
	if kind != "" && !upstreamstate.ValidCheckKind(kind) {
		return rejectRequest(errors.New("未知的检测类型"))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return rejectRequest(errors.New("程序正在退出"))
	}
	if c.monitor == nil {
		return rejectRequest(errors.New("上游监测尚未启动"))
	}
	s := c.monitor.Snapshot()
	if c.monitorManual || s.Manual || (kind == "" && s.Checking) {
		return nil
	}
	if kind == "" && (time.Since(s.Started) < 10*time.Second || time.Since(c.monitorManualAt) < 10*time.Second) {
		return rejectRequest(errors.New("两轮检测开始至少间隔 10 秒"))
	}
	c.monitorManual, c.monitorManualAt = true, time.Now()
	done := make(chan struct{})
	c.monitorManualDone = done
	m := c.monitor
	go func() {
		defer close(done)
		if kind == "" {
			if err := m.Check(context.Background()); err == nil {
				_ = c.requestResourceSample(context.Background(), resourceSampleRequest{})
			}
		} else {
			_ = m.CheckSelected(context.Background(), kind)
		}
		c.mu.Lock()
		c.monitorManual = false
		c.mu.Unlock()
	}()
	return nil
}
