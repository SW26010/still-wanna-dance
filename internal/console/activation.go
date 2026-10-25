package console

import (
	"errors"
	"fmt"
	"os"
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
		return errors.New("正在启用加速，请等待当前操作完成")
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
				err = errors.Join(err, fmt.Errorf("未能撤销本次游戏接入：%w", rollbackErr))
			}
		}
	}()
	c.activationPhase("hosts")
	// The shared hosts controller checks both the mapping and the ability to
	// restore it before deciding whether authorization is necessary.
	if err = change("enable"); err != nil {
		return fmt.Errorf("游戏接入失败：%w", err)
	}
	if hosts = inspect(); !hosts.Ready {
		return fmt.Errorf("hosts 接入检查未通过：%s", hosts.Message)
	}
	c.activationPhase("starting")
	if err = c.startLocked(); err != nil {
		return err
	}
	if hosts = inspect(); !hosts.Ready {
		return fmt.Errorf("网关启动后的 hosts 接入检查未通过：%s", hosts.Message)
	}
	c.mu.Lock()
	if c.httpServer == nil || c.videoListener == nil || c.https == nil {
		c.mu.Unlock()
		return errors.New("视频网关尚未就绪")
	}
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
	err := c.changeHostsLocked(action)
	if action == "disable" && err == nil {
		c.mu.Lock()
		c.activation.FirstRequest = time.Time{}
		if !c.activation.Started.IsZero() {
			c.activation.Phase = "stopped"
		}
		c.mu.Unlock()
	}
	return err
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

// Starting the gateway explicitly may reuse an existing managed connection.
// Prepare its exit cleanup here, while this user operation can request UAC.
// Automatic acceleration uses the same hosts controller through enableAcceleration.
func (c *Console) startForUser() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if err := c.startLocked(); err != nil {
		return err
	}
	data, err := os.ReadFile(c.hosts.path)
	if err != nil {
		return err
	}
	if status := inspectHosts(string(data)); status.Ready && status.Managed {
		return c.changeHostsLocked("enable")
	}
	return nil
}
