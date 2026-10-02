package console

import (
	"errors"
	"fmt"
	"time"
)

// Activation is a process-local observation window, shared by all console tabs.
// It is deliberately independent of persisted traffic and download statistics.
type Activation struct {
	Phase        string    `json:"phase"`
	Started      time.Time `json:"started"`
	ReadyAt      time.Time `json:"readyAt"`
	FirstRequest time.Time `json:"firstRequest"`
	Error        string    `json:"error"`
}

func (c *Console) activationPhase(phase string) {
	c.mu.Lock()
	c.activation.Phase = phase
	c.mu.Unlock()
}

// Inject only the hosts boundary in tests; service startup uses the real lifecycle.
func (c *Console) enableAcceleration(inspect func() HostsStatus, change func(string) error) (err error) {
	if !c.activationMu.TryLock() {
		return errors.New("启用向导仍在执行，请等待当前操作完成")
	}
	defer c.activationMu.Unlock()
	// Keep tray, settings and individual hosts operations from interleaving.
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	c.activationGeneration++
	c.activation = Activation{Phase: "checking", Started: time.Now()}
	c.mu.Unlock()
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.activation.Phase, c.activation.Error = "failed", err.Error()
			c.mu.Unlock()
		}
	}()
	hosts := inspect()
	if hosts.Conflict {
		return fmt.Errorf("接入检查失败：%s", hosts.Message)
	}
	c.activationPhase("starting")
	if err = c.startLocked(); err != nil {
		return err
	}
	c.activationPhase("hosts")
	if !hosts.Ready {
		if err = change("enable"); err != nil {
			return fmt.Errorf("CDN 已运行，但 hosts 接入未完成：%w；处理后可重试，也可在单项管理中关闭 CDN", err)
		}
	}
	if hosts = inspect(); !hosts.Ready {
		return fmt.Errorf("CDN 已运行，但 hosts 检查未通过：%s", hosts.Message)
	}
	c.mu.Lock()
	c.activation.Phase, c.activation.ReadyAt = "waiting", time.Now()
	c.mu.Unlock()
	return nil
}

func (c *Console) beginVideoRequest(arrived time.Time) func() {
	c.mu.Lock()
	generation := c.activationGeneration
	eligible := c.httpServer != nil && c.activation.Phase == "waiting" && !arrived.Before(c.activation.ReadyAt)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if eligible && generation == c.activationGeneration && c.httpServer != nil && c.activation.Phase == "waiting" &&
			(c.activation.FirstRequest.IsZero() || arrived.Before(c.activation.FirstRequest)) {
			c.activation.FirstRequest = arrived
		}
	}
}

func (c *Console) changeHosts(action string) error {
	if action == "enable" && !c.termsAccepted() {
		return errors.New("请先阅读并同意使用条款")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	err := changeHosts(action)
	if action == "disable" {
		c.mu.Lock()
		c.activation.FirstRequest = time.Time{}
		if !c.activation.Started.IsZero() {
			c.activation.Phase = "stopped"
		}
		c.mu.Unlock()
	}
	return err
}
