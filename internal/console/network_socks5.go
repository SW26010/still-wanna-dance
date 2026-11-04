package console

import (
	"context"
	"errors"
	"time"

	"still-wanna-dance/internal/upstreamnet"
)

const socks5ServiceInterval = 30 * time.Second

type socks5ServiceStatus struct {
	Checking      bool      `json:"checking"`
	Closed        bool      `json:"closed"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	Authenticated bool      `json:"authenticated"`
	LatencyMS     *float64  `json:"latencyMS"`
	ObservedAt    time.Time `json:"observedAt"`
	NextCheck     time.Time `json:"nextCheck"`
}

// Called under c.mu only after consent/startup. State reads never start probes.
func (c *Console) startSOCKS5ServiceLocked() {
	n := c.networkDiagnostics
	s := c.settings
	if s.UpstreamMode != "proxy" || s.ProxyBackend != "external-socks5" || n.socks5Done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.socks5Cancel, n.socks5Done = cancel, make(chan struct{})
	n.socks5Wake = make(chan struct{}, 1)
	n.socks5.Checking = true
	go func() {
		defer close(n.socks5Done)
		for {
			c.mu.Lock()
			if ctx.Err() != nil {
				c.mu.Unlock()
				return
			}
			n.socks5.Checking, n.socks5.NextCheck = true, time.Time{}
			// A manual trigger racing the automatic deadline joins this check.
			select {
			case <-n.socks5Wake:
			default:
			}
			c.mu.Unlock()
			started := time.Now()
			var authenticated bool
			var err error
			if s.proxySecretError != "" {
				err = errors.New(unavailableProxySecret)
			} else {
				authenticated, err = upstreamnet.CheckSOCKS5Service(ctx, s.SOCKS5Address, s.SOCKS5Username, s.SOCKS5Password)
			}
			finished := time.Now()
			c.mu.Lock()
			if ctx.Err() != nil {
				c.mu.Unlock()
				return
			}
			n.socks5 = socks5ServiceStatus{State: "available", Authenticated: authenticated, ObservedAt: finished, NextCheck: finished.Add(socks5ServiceInterval)}
			if err != nil {
				n.socks5.State, n.socks5.Error = "failed", err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					n.socks5.Error = "SOCKS5 服务检查超时"
				}
			} else {
				ms := float64(finished.Sub(started)) / float64(time.Millisecond)
				n.socks5.LatencyMS = &ms
			}
			c.mu.Unlock()
			timer := time.NewTimer(socks5ServiceInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-n.socks5Wake:
				timer.Stop()
			}
		}
	}()
}

func (c *Console) checkSOCKS5Service() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.UpstreamMode != "proxy" || c.settings.ProxyBackend != "external-socks5" {
		return errors.New("当前上游未使用外部 SOCKS5")
	}
	if _, err := c.networkCalibrationLocked(); err != nil {
		return err
	}
	n := c.networkDiagnostics
	if n.socks5Done == nil {
		c.startSOCKS5ServiceLocked()
	} else if !n.socks5.Checking {
		n.socks5.Checking, n.socks5.NextCheck = true, time.Time{}
		n.socks5Wake <- struct{}{}
	}
	return nil
}

func (c *Console) socks5ServiceSnapshotLocked() *socks5ServiceStatus {
	if c.settings.UpstreamMode != "proxy" || c.settings.ProxyBackend != "external-socks5" {
		return nil
	}
	var status socks5ServiceStatus
	if c.networkDiagnostics != nil {
		status = c.networkDiagnostics.socks5
	}
	return &status
}
