package desktop

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	vrchatDiscoveryInterval = 2 * time.Second
	vrchatExitConfirmation  = 2 * time.Second
)

type processWaiter interface {
	Wait(context.Context) error
	Close() error
}

// WaitForVRChatExit first observes a game, then waits until no game remains in
// this session. A live process is waited on without periodic enumeration. The
// caller owns cancellation; this function releases all native resources before
// returning. A nil result means exit was confirmed, never an observation error.
func WaitForVRChatExit(ctx context.Context) error {
	return waitForVRChatExit(ctx, findVRChatProcess)
}

func waitForVRChatExit(ctx context.Context, find func() (processWaiter, error)) error {
	seen, absent, failed := false, false, false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		process, err := find()
		if err == nil && process != nil {
			if !seen {
				slog.Info("vrchat_detected")
			}
			seen, absent = true, false
			err = errors.Join(process.Wait(ctx), process.Close())
			if err == nil {
				failed = false
				// A known process ended. Discover any remaining or replacement
				// instance before starting the one-shot exit confirmation.
				continue
			}
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		delay := vrchatDiscoveryInterval
		if err != nil {
			if !failed {
				slog.Warn("vrchat_process_check_failed", "error", err)
			}
			failed, absent = true, false
		} else {
			failed = false
			if seen {
				if absent {
					return nil
				}
				absent, delay = true, vrchatExitConfirmation
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
