package console

import (
	"context"
	"errors"
	"time"

	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/upstreamstate"
)

// StartUpstreamMonitor attaches the canonical monitor to the console lifetime.
// A pending start is completed by acceptTerms; constructing/reading never probes.
func (c *Console) StartUpstreamMonitor() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monitorRequested = true
	return c.startMonitorLocked()
}
func (c *Console) startMonitorLocked() error {
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if !c.monitorRequested || c.terms.Version != legal.Version || c.terms.Hash != legal.Hash() || c.terms.AcceptedAt.IsZero() {
		return nil
	}
	if c.monitor == nil {
		m, err := upstreamstate.NewMonitor(upstreamstate.Options{ThroughputStatePath: c.configPath + ".throughput.json"})
		if err != nil {
			return err
		}
		c.monitor = m
	}
	return c.monitor.Start()
}
func (c *Console) monitorSnapshotLocked() upstreamstate.Status {
	if c.monitor == nil {
		return upstreamstate.Status{Policy: upstreamstate.DefaultPolicy(), Results: []upstreamstate.Result{}}
	}
	s := c.monitor.Snapshot()
	s.Checking = s.Checking || c.monitorManual
	return s
}
func (c *Console) requestMonitorCheck() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.monitor == nil {
		return errors.New("上游监测尚未启动")
	}
	s := c.monitor.Snapshot()
	if s.Checking || c.monitorManual {
		return nil
	}
	if time.Since(s.Started) < 10*time.Second || time.Since(c.monitorManualAt) < 10*time.Second {
		return errors.New("两轮检测开始至少间隔 10 秒")
	}
	c.monitorManual, c.monitorManualAt = true, time.Now()
	done := make(chan struct{})
	c.monitorManualDone = done
	m := c.monitor
	go func() {
		defer close(done)
		_ = m.Check(context.Background())
		c.mu.Lock()
		c.monitorManual = false
		c.mu.Unlock()
	}()
	return nil
}
