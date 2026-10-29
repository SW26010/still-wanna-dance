package console

import (
	"errors"
	"fmt"
	"time"
)

// Activation is a process-local observation window, shared by all console tabs.
// It is deliberately independent of persisted traffic and download statistics.
type Activation struct {
	Phase                    string    `json:"phase"`
	Started                  time.Time `json:"started"`
	ReadyAt                  time.Time `json:"readyAt"`
	FirstRequest             time.Time `json:"firstRequest"`
	Error                    string    `json:"error"`
	ConnectionError          string    `json:"connectionError,omitempty"`
	restorePermissionMissing bool      // identifies the current connection error without parsing its message
}

func (c *Console) activationPhase(phase string) {
	c.mu.Lock()
	c.activation.Phase = phase
	c.mu.Unlock()
}

// Inject only the hosts boundary in tests; service startup uses the real lifecycle.
func (c *Console) enableAcceleration(inspect func() HostsStatus, change func(string) error) (err error) {
	if !c.activationMu.TryLock() {
		return errors.New("正在处理加速，请等待当前操作完成")
	}
	defer c.activationMu.Unlock()
	// Keep tray, settings and individual hosts operations from interleaving.
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if !c.termsAccepted() {
		return errors.New("请先阅读并同意使用条款")
	}
	c.mu.Lock()
	wasRunning := c.httpServer != nil
	c.activationGeneration++
	c.activation = Activation{Phase: "checking", Started: time.Now()}
	c.mu.Unlock()
	var connectionErr error
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.activation.Phase, c.activation.Error = "failed", err.Error()
			if connectionErr != nil {
				c.activation.ConnectionError = connectionErr.Error()
			}
			c.mu.Unlock()
		}
	}()
	hosts := inspect()
	if hosts.Conflict {
		connectionErr = fmt.Errorf("接入检查失败：%s", hosts.Message)
		return connectionErr
	}
	wasReady := hosts.Ready
	defer func() {
		if err == nil {
			return
		}
		if !wasRunning {
			c.mu.Lock()
			if c.httpServer != nil {
				c.stopLocked()
			}
			c.mu.Unlock()
		}
		if !wasReady {
			if rollbackErr := change("rollback"); rollbackErr != nil {
				rollbackErr = fmt.Errorf("未能撤销本次游戏接入：%w", rollbackErr)
				connectionErr = errors.Join(connectionErr, rollbackErr)
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	c.activationPhase("hosts")
	// Only missing mappings require authorization here. Restoration permission
	// for an existing connection is prepared separately at the user's request.
	if err = change("enable"); err != nil {
		connectionErr = fmt.Errorf("游戏接入失败：%w", err)
		return connectionErr
	}
	if hosts = inspect(); !hosts.Ready {
		connectionErr = fmt.Errorf("hosts 接入检查未通过：%s", hosts.Message)
		return connectionErr
	}
	c.activationPhase("starting")
	if err = c.startLocked(); err != nil {
		return err
	}
	if hosts = inspect(); !hosts.Ready {
		connectionErr = fmt.Errorf("网关启动后的 hosts 接入检查未通过：%s", hosts.Message)
		return connectionErr
	}
	c.mu.Lock()
	if c.httpServer == nil || c.videoListener == nil || c.https == nil {
		err = errors.New("视频网关尚未就绪")
		if c.cdnError == "" {
			c.cdnError = err.Error()
		}
		c.mu.Unlock()
		return err
	}
	c.activation.Phase, c.activation.ReadyAt = "waiting", time.Now()
	c.mu.Unlock()
	return nil
}

// Stop only the gateway and its queue. The shared engine and authorized hosts
// helper live until application exit, so resuming can reuse both.
func (c *Console) disableAcceleration() error {
	if !c.activationMu.TryLock() {
		return errors.New("正在处理加速，请等待当前操作完成")
	}
	defer c.activationMu.Unlock()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return errors.New("程序正在退出")
	}
	restore := c.settings.RestoreHostsOnExit
	c.activation.Phase, c.activation.Error = "stopping", ""
	c.activation.ConnectionError = ""
	c.activation.restorePermissionMissing = false
	c.mu.Unlock()
	var err error
	if restore {
		if hostsErr := c.hosts.change("disable", restore); hostsErr != nil {
			err = fmt.Errorf("加速已停止，但未能恢复游戏直连：%w", hostsErr)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	c.cdnError = ""
	c.activation.Phase = "stopped"
	if err != nil {
		c.activation.Error = err.Error()
		c.activation.ConnectionError = err.Error()
		c.activation.restorePermissionMissing = errors.Is(err, errHostsRestorePermission)
	}
	return err
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

// Caller owns lifecycleMu. All explicit connection operations share this
// preflight and helper, regardless of the configured exit policy.
func (c *Console) changeHostsLocked(action string) error {
	c.mu.Lock()
	closing, restore := c.closing, c.settings.RestoreHostsOnExit
	c.mu.Unlock()
	if closing {
		return errors.New("程序正在退出")
	}
	return c.hosts.change(action, restore)
}

// Prepare restoration without enabling acceleration or changing hosts.
func (c *Console) prepareHostsRestore() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	closing, restore := c.closing, c.settings.RestoreHostsOnExit
	c.mu.Unlock()
	if closing {
		return errors.New("程序正在退出")
	}
	if !restore {
		return nil
	}
	if err := c.hosts.prepareRestore(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hosts.prepared.Load() && c.activation.restorePermissionMissing {
		c.activation.ConnectionError = ""
		c.activation.restorePermissionMissing = false
	}
	return nil
}
