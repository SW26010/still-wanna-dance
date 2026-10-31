package console

import (
	"context"
	"errors"
	"time"

	"still-wanna-dance/internal/upstreamstate"
)

// Direct Monitor responses may feed the independent business synchronization.
func (c *Console) newUpstreamMonitor(o upstreamstate.Options) (*upstreamstate.Monitor, error) {
	o.OnCatalog = func(ctx context.Context, responses []upstreamstate.CatalogResponse) {
		c.syncCatalogs(ctx, responses)
	}
	o.OnPlaybackURL = c.syncPlaybackURL
	o.Resources = c.resources
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
func (c *Console) startUpstreamServicesLocked() error {
	if c.closing {
		return errors.New("程序正在退出")
	}
	if !c.upstreamRequested || !c.terms.valid() {
		return nil
	}
	if err := c.startAutomaticConnectivityLocked(); err != nil {
		return err
	}
	if c.settings.UpstreamMode != "direct" {
		return nil
	}
	if c.monitor == nil {
		m, err := c.newUpstreamMonitor(upstreamstate.Options{
			ThroughputStatePath: c.files().State("throughput"),
			Policy:              upstreamstate.Policy{ThroughputInterval: time.Duration(c.settings.ThroughputIntervalMinutes) * time.Minute},
		})
		if err != nil {
			return err
		}
		c.monitor = m
	}
	return c.monitor.Start()
}

func (c *Console) monitorSnapshotLocked() upstreamstate.Status {
	if c.monitor == nil {
		policy := upstreamstate.DefaultPolicy()
		if c.settings.ThroughputIntervalMinutes > 0 {
			policy.ThroughputInterval = time.Duration(c.settings.ThroughputIntervalMinutes) * time.Minute
		}
		return upstreamstate.Status{Policy: policy, Results: []upstreamstate.Result{}}
	}
	s := c.monitor.Snapshot()
	s.Checking = s.Checking || c.monitorManual
	s.Manual = s.Manual || c.monitorManual
	return s
}
func (c *Console) requestMonitorCheck(selected ...upstreamstate.CheckKind) error {
	var kind upstreamstate.CheckKind
	if len(selected) > 0 {
		kind = selected[0]
	}
	if kind != "" && !upstreamstate.ValidCheckKind(kind) {
		return errors.New("未知的检测类型")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("程序正在退出")
	}
	if c.monitor == nil {
		return errors.New("上游监测尚未启动")
	}
	s := c.monitor.Snapshot()
	if c.monitorManual || s.Manual || (kind == "" && s.Checking) {
		return nil
	}
	if kind == "" && (time.Since(s.Started) < 10*time.Second || time.Since(c.monitorManualAt) < 10*time.Second) {
		return errors.New("两轮检测开始至少间隔 10 秒")
	}
	c.monitorManual, c.monitorManualAt = true, time.Now()
	done := make(chan struct{})
	c.monitorManualDone = done
	m := c.monitor
	go func() {
		defer close(done)
		if kind == "" {
			_ = m.Check(context.Background())
		} else {
			_ = m.CheckSelected(context.Background(), kind)
		}
		c.mu.Lock()
		c.monitorManual = false
		c.mu.Unlock()
	}()
	return nil
}
