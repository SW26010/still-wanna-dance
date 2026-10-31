package console

import (
	"context"
	"errors"
	"log/slog"

	"still-wanna-dance/internal/desktop"
)

// WatchVRChat observes the game independently of gateway and log monitoring.
// The application owner runs it once and waits for it after BeginShutdown.
func (c *Console) WatchVRChat() { c.watchVRChat(desktop.WaitForVRChatExit) }

func (c *Console) watchVRChat(wait func(context.Context) error) {
	c.mu.Lock()
	enabled := c.settings.ExitWithVRChat
	c.mu.Unlock()
	if !enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-c.lifecycle.stop:
		case <-c.ShutdownRequested():
		case <-ctx.Done():
		}
		cancel()
	}()
	err := wait(ctx)
	cancel()
	<-stopped
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("vrchat_process_check_failed", "error", err)
		}
		return
	}
	// Cleanup alone must not be turned into a planned exit.
	select {
	case <-c.lifecycle.stop:
		return
	case <-c.ShutdownRequested():
		return
	default:
	}
	slog.Info("vrchat_exited")
	c.RequestExit()
}
