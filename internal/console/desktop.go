package console

import (
	"fmt"
	"log/slog"
	"stepstash/internal/desktop"
)

// DesktopState and DesktopCommand share exactly the web console's engine.
func (c *Console) DesktopState() desktop.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return desktop.State{CDN: c.httpServer != nil, Batch: c.batch.Running}
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
	case desktop.ToggleCDN:
		if c.DesktopState().CDN {
			return c.stop()
		}
		return c.start()
	case desktop.CancelBatch:
		c.mu.Lock()
		if c.batchCancel != nil {
			c.batchCancel()
		}
		c.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("unknown desktop command: %d", id)
	}
}
