package console

import (
	"fmt"
	"log/slog"
	"still-wanna-dance/internal/desktop"
)

// DesktopState and DesktopCommand use the home page's readiness and activation flow.
func (c *Console) DesktopState() desktop.State {
	c.mu.Lock()
	s := desktop.State{CDN: c.httpServer != nil, Batch: c.batch.Running, TermsAccepted: c.terms.valid(), Theme: c.settings.Theme}
	restore := c.settings.RestoreHostsOnExit
	switch c.activation.Phase {
	case "checking", "hosts", "starting":
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
	default:
		return fmt.Errorf("unknown desktop command: %d", id)
	}
}
