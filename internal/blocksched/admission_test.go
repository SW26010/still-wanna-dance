package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestBlockingCountsTowardOrdinaryAdmissionWithoutPreemption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 4, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		started, canceled := make(chan string, 16), make(chan string, 16)
		cleanup := make(chan struct{})
		endReads := make(map[string]func())
		submit := func(key string, blocking bool, ready time.Duration) (*Task, *Demand, chan struct{}) {
			release := make(chan struct{})
			if blocking {
				endReads[key] = waitForKey(t, s, key)
			}
			j, demand, _ := s.Schedule(key, 1<<20, func(ctx context.Context, _ *Progress) Result {
				started <- key
				select {
				case <-release:
					if key == "a" {
						select {
						case <-cleanup:
						case <-ctx.Done():
							canceled <- key
						}
					}
				case <-ctx.Done():
					canceled <- key
				}
				return Result{}
			}, need(key, ready, time.Hour))
			return j, demand, release
		}
		var ordinary []*Task
		var release []chan struct{}
		for _, key := range []string{"a", "b", "c", "d"} {
			j, _, gate := submit(key, false, time.Second)
			ordinary, release = append(ordinary, j), append(release, gate)
		}
		time.Sleep(750 * time.Millisecond)
		synctest.Wait()
		if len(started) != 4 {
			t.Fatal("ordinary capacity did not reach four", len(started))
		}
		for range 4 {
			<-started
		}
		now := time.Now()
		one, oneDemand, _ := submit("blocking-one", true, 0)
		submit("blocking-two", true, 0)
		synctest.Wait()
		if len(started) != 2 || !time.Now().Equal(now) {
			t.Fatal("Blocking waited for quota or ramp")
		}
		for range 2 {
			<-started
		}
		duplicate, _, _ := s.Schedule("blocking-one", 1, nil, need("blocking-one", time.Minute, time.Hour))
		if duplicate != one {
			t.Fatal("running block was duplicated")
		}
		// Losing Blocking while an ordinary contribution remains must not restart.
		endReads["blocking-one"]()
		oneDemand.Update(need("blocking-one", time.Minute, time.Hour))
		submit("later", false, 2*time.Second)
		submit("earlier", false, time.Second)
		time.Sleep(time.Second)
		synctest.Wait()
		assertState := func(active int) {
			t.Helper()
			s.call(func(st *state) {
				if st.active != active || st.control.open != 4 {
					t.Errorf("active/open = %d/%d, want %d/4", st.active, st.control.open, active)
				}
			})
			if len(canceled) != 0 || len(started) != 0 {
				t.Fatal("priority change canceled, restarted, or overfilled work", len(canceled), len(started))
			}
		}
		assertState(6)
		close(release[0])
		synctest.Wait()
		assertState(6) // Body/cleanup still holds the active slot.
		close(cleanup)
		<-ordinary[0].Done()
		synctest.Wait()
		assertState(5)
		close(release[1])
		<-ordinary[1].Done()
		synctest.Wait()
		assertState(4) // Equal to open still cannot admit an ordinary block.
		close(release[2])
		<-ordinary[2].Done()
		synctest.Wait()
		if len(started) != 1 || <-started != "earlier" {
			t.Fatal("ordinary refill did not select the earliest ReadyBy")
		}
		assertState(4) // One Blocking remains, yet the free slot is reusable.
	})
}

func TestQueuedPromotionBypassesQuotaAndKeepsSharedExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, GrowInterval: time.Hour})
		defer s.Close()
		started := make(chan string, 4)
		run := func(key string) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				<-ctx.Done()
				return Result{Err: context.Canceled}
			}
		}
		_, _, _ = s.Schedule("old", 1, run("old"), need("old", time.Second, 24*time.Hour))

		<-started
		_, d, _ := s.Schedule("next", 1, run("next"), need("next", time.Minute, 24*time.Hour))

		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("ordinary task bypassed capacity")
		}
		now := time.Now()
		endRead := waitForKey(t, s, "next")
		if !d.Update(need("next", 0, 24*time.Hour)) {
			t.Fatal("promotion failed")
		}
		if got := <-started; got != "next" || !time.Now().Equal(now) {
			t.Fatal("Blocking promotion waited", got)
		}
		endRead()
		d.Update(need("next", time.Minute, 24*time.Hour))
		waitForKey(t, s, "next")
		d.Update(need("next", 0, 24*time.Hour))
		time.Sleep(time.Second)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("changing Blocking restarted an in-flight task")
		}
	})
}

func TestUrgentOrdinaryDeadlineWaitsForNaturalCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan string, 3)
		release := make(chan struct{})
		_, _, _ = s.Schedule("headers", 25, func(ctx context.Context, _ *Progress) Result {
			started <- "old"
			select {
			case <-release:
			case <-ctx.Done():
				t.Error("urgent ordinary task canceled in-flight work")
			}
			return Result{}
		}, need("old", time.Second, time.Hour))

		<-started
		for _, key := range []string{"later", "due"} {
			ready := time.Minute
			if key == "due" {
				ready = time.Second
			}
			_, _, _ = s.Schedule(key, 25, func(context.Context, *Progress) Result { started <- key; return Result{} }, need(key, ready, time.Hour))

		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("deadline preempted an active ordinary block")
		}
		close(release)
		if got := <-started; got != "due" {
			t.Fatal("ordinary queue ignored ReadyBy", got)
		}
		if got := <-started; got != "later" {
			t.Fatal(got)
		}
	})
}
