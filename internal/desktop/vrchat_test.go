package desktop

import (
	"context"
	"errors"
	"sync"
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

// synctest.Wait orders completed observations before the test reads them, but
// fake time advancing does not order a new observation after a test-side write.
type waitingTestDiscovery struct {
	mu      sync.Mutex
	process processWaiter
	err     error
	calls   int
}

func (d *waitingTestDiscovery) set(process processWaiter, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.process, d.err = process, err
}

func (d *waitingTestDiscovery) find() (processWaiter, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return d.process, d.err
}

func (d *waitingTestDiscovery) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func TestVRChatExitWaitsWithoutPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		process := &waitingTestProcess{exit: make(chan struct{})}
		discovery := &waitingTestDiscovery{}
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, discovery.find)
		}()
		synctest.Wait()
		// No prior game: stay open. The next discovery attaches to the new game.
		discovery.set(process, nil)
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		if discovery.count() != 2 || process.closed {
			t.Fatal("game was not attached", discovery.count(), process.closed)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if discovery.count() != 2 {
			t.Fatal("enumerated processes while the known game was alive", discovery.count())
		}
		discovery.set(nil, nil)
		close(process.exit)
		synctest.Wait()
		if discovery.count() != 3 || !process.closed {
			t.Fatal("exit did not release the handle and recheck other instances")
		}
		select {
		case err := <-done:
			t.Fatal("exited before restart confirmation", err)
		default:
		}
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || discovery.count() != 4 {
			t.Fatal(err, discovery.count())
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
		discovery := &waitingTestDiscovery{process: first}
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, discovery.find)
		}()
		synctest.Wait()
		discovery.set(second, nil)
		close(first.exit)
		synctest.Wait()
		if discovery.count() != 2 || !first.closed || second.closed {
			t.Fatal("did not follow the remaining instance")
		}
		discovery.set(nil, nil)
		close(second.exit)
		synctest.Wait()
		time.Sleep(vrchatExitConfirmation / 2)
		discovery.set(restarted, nil)
		time.Sleep(vrchatExitConfirmation / 2)
		synctest.Wait()
		if discovery.count() != 4 || restarted.closed {
			t.Fatal("restart did not cancel exit confirmation")
		}
		discovery.set(nil, nil)
		close(restarted.exit)
		synctest.Wait()
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || discovery.count() != 6 || !restarted.closed {
			t.Fatal(err, discovery.count(), restarted.closed)
		}
	})
}

func TestVRChatExitObservationErrorsRecover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failure := errors.New("observation unavailable")
		process := &waitingTestProcess{exit: make(chan struct{}), err: failure}
		discovery := &waitingTestDiscovery{err: failure}
		done := make(chan error, 1)
		go func() {
			done <- waitForVRChatExit(ctx, discovery.find)
		}()
		synctest.Wait()
		discovery.set(nil, nil)
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		// An unknown first observation never arms automatic exit.
		discovery.set(process, nil)
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		discovery.set(nil, nil)
		close(process.exit) // a failed native wait must release its handle and retry
		synctest.Wait()
		if !process.closed {
			t.Fatal("failed wait leaked the process handle")
		}
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		discovery.set(nil, failure) // confirmation failure resets the absent evidence
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		discovery.set(nil, nil)
		time.Sleep(vrchatDiscoveryInterval)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatal("unknown state was treated as absence", err)
		default:
		}
		time.Sleep(vrchatExitConfirmation)
		synctest.Wait()
		if err := <-done; err != nil || discovery.count() != 7 {
			t.Fatal(err, discovery.count())
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
