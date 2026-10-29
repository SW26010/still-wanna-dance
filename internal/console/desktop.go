package console

import (
	"fmt"
	"log/slog"
	"still-wanna-dance/internal/desktop"
)

// This controls automatic browser opening for a new console process. Explicit
// tray/open and second-instance activation remain available independently.
// Missing or invalid consent must expose the existing confirmation flow.
func (c *Console) OpenWebUIOnLaunch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.terms.valid() || c.settings.AutoOpenWebUI
}

// DesktopState and DesktopCommand use the home page's readiness and activation flow.
func (c *Console) DesktopState() desktop.State {
	c.mu.Lock()
	s := desktop.State{CDN: c.httpServer != nil, Batch: c.batch.Running, TermsAccepted: c.terms.valid(), Theme: c.settings.Theme}
	restore := c.settings.RestoreHostsOnStopOrExit
	switch c.activation.Phase {
	case "checking", "hosts", "starting", "stopping":
		s.Activating = true
	}
	c.mu.Unlock()
	// Status reads must remain independent of lifecycleMu while UAC is pending.
	hosts := c.hosts.status()
	s.Ready = s.CDN && hosts.Ready
	s.NeedsExitPreparation = restore && hosts.Managed && !hosts.ExitRestorePrepared
	return s
}

func (c *Console) DesktopCommand(id int) (err error) {
	defer func() {
		if err != nil {
			slog.Error("tray_action_failed", "command", id, "error", err)
		} else {
			slog.Info("tray_action_completed", "command", id)
		}
	}()
	switch id {
	case desktop.StartAcceleration:
		return c.enableAcceleration(c.hosts.status, c.changeHostsLocked)
	case desktop.StopAcceleration:
		return c.disableAcceleration()
	default:
		return fmt.Errorf("unknown desktop command: %d", id)
	}
}
