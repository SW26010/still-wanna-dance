package console

import (
	"fmt"
	"stepstash/internal/desktop"
)

// DesktopState and DesktopCommand share exactly the web console's engine.
func (c *Console) DesktopState() desktop.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return desktop.State{CDN: c.httpServer != nil, Batch: c.batch.Running}
}

func (c *Console) DesktopCommand(id int) error {
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
