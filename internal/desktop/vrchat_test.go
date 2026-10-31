package desktop

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type waitingTestProcess struct {
	exit   chan struct{}
	closed bool
	err    error
}

func (p *waitingTestProcess) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.exit:
		return p.err
	}
}

func (p *waitingTestProcess) Close() error { p.closed = true; return nil }

func TestVRChatExitWaitsWithoutPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		process := &waitingTestProcess{exit: make(chan struct{})}
		var available processWaiter
		calls := 0
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, func() (processWaiter, error) { calls++; return available, nil })
		}()
		synctest.Wait()
		// No prior game: stay open. The next discovery attaches to the new game.
		available = process
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		if calls != 2 || process.closed {
			t.Fatal("game was not attached", calls, process.closed)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls != 2 {
			t.Fatal("enumerated processes while the known game was alive", calls)
		}
		available = nil
		close(process.exit)
		synctest.Wait()
		if calls != 3 || !process.closed {
			t.Fatal("exit did not release the handle and recheck other instances")
		}
		select {
		case err := <-done:
			t.Fatal("exited before restart confirmation", err)
		default:
		}
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || calls != 4 {
			t.Fatal(err, calls)
		}
	})
}

func TestVRChatExitWaitsForOtherInstancesAndRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first := &waitingTestProcess{exit: make(chan struct{})}
		second := &waitingTestProcess{exit: make(chan struct{})}
		restarted := &waitingTestProcess{exit: make(chan struct{})}
		var available processWaiter = first
		calls := 0
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, func() (processWaiter, error) { calls++; return available, nil })
		}()
		synctest.Wait()
		available = second
		close(first.exit)
		synctest.Wait()
		if calls != 2 || !first.closed || second.closed {
			t.Fatal("did not follow the remaining instance")
		}
		available = nil
		close(second.exit)
		synctest.Wait()
		time.Sleep(vrchatExitConfirmation / 2)
		available = restarted
		time.Sleep(vrchatExitConfirmation / 2)
		synctest.Wait()
		if calls != 4 || restarted.closed {
			t.Fatal("restart did not cancel exit confirmation")
		}
		available = nil
		close(restarted.exit)
		synctest.Wait()
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || calls != 6 || !restarted.closed {
			t.Fatal(err, calls, restarted.closed)
		}
	})
}

func TestVRChatExitObservationErrorsRecover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failure := errors.New("observation unavailable")
		process := &waitingTestProcess{exit: make(chan struct{}), err: failure}
		var available processWaiter
		findErr := failure
		calls := 0
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, func() (processWaiter, error) { calls++; return available, findErr })
		}()
		synctest.Wait()
		findErr = nil
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		// An unknown first observation never arms automatic exit.
		available = process
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		available = nil
		close(process.exit) // a failed native wait must release its handle and retry
		synctest.Wait()
		if !process.closed {
			t.Fatal("failed wait leaked the process handle")
		}
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		findErr = failure // confirmation failure resets the absent evidence
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		findErr = nil
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatal("unknown state was treated as absence", err)
		default:
		}
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || calls != 7 {
			t.Fatal(err, calls)
		}
	})
}

func TestVRChatExitWaitCancellationReleasesResources(t *testing.T) {
	for _, running := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			process := &waitingTestProcess{exit: make(chan struct{})}
			calls := 0
			done := make(chan error, 1)
			go func() {
				done <- waitForVRChatExit(ctx, func() (processWaiter, error) {
					calls++
					if running {
						return process, nil
					}
					return nil, nil
				})
			}()
			synctest.Wait()
			cancel()
			synctest.Wait()
			if err := <-done; !errors.Is(err, context.Canceled) || process.closed != running || calls != 1 {
				t.Fatal(err, process.closed, calls)
			}
		})
	}
}
