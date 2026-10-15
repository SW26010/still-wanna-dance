package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestWindowBoundaryAndDeadlineDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ready           time.Duration
		wait            time.Duration
		urgent, overdue bool
	}{
		{"outside", 40 * time.Second, 7 * time.Second, false, false},
		{"window_equality", 33 * time.Second, 0, false, false},
		{"latest_start_equality", 3 * time.Second, 0, true, false},
		{"deadline_equality", 0, 0, true, false},
		{"past_deadline", -time.Second, 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var events []Event
				s := New(Config{SoftLimit: 16, GrowInterval: time.Second, Observe: func(e Event) { events = append(events, e) }})
				defer s.Close()
				started := make(chan time.Time, 1)
				j, _ := s.Submit("one", standardBlockSize, func(ctx context.Context, _ *Progress) Result {
					started <- time.Now()
					<-ctx.Done()
					return Result{}
				})
				now := time.Now()
				j.Add(Need{Owner: "one", ReadyBy: now.Add(tc.ready), LeaseUntil: now.Add(time.Hour)})
				synctest.Wait()
				if tc.wait > 0 {
					if len(started) != 0 {
						t.Fatal("started outside window")
					}
					s.call(func(st *state) {
						if got := st.nextControlEvent(now); !got.Equal(now.Add(tc.wait)) {
							t.Error("incorrect window timer", got.Sub(now))
						}
					})
				}
				if at := <-started; at.Sub(now) != tc.wait {
					t.Fatal("boundary start", at.Sub(now))
				}
				synctest.Wait()
				s.call(func(st *state) {
					if st.control.open != 1 || !st.nextControlEvent(time.Now()).IsZero() {
						t.Error("running task requested capacity or periodic wakeups")
					}
				})
				for _, e := range events {
					if e.Kind == "started" && (e.StartUrgent != tc.urgent || e.Overdue != tc.overdue) {
						t.Fatalf("incorrect deadline diagnosis: %+v", e)
					}
				}
			})
		})
	}
}

func TestWindowBacklogDrivesGrowthAndIdleResetsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 4, GrowInterval: time.Second})
		defer s.Close()
		started := make(chan string, 8)
		release := make(chan struct{})
		var demands []*Demand
		for _, key := range []string{"a", "b", "c", "d"} {
			j, _ := s.Submit(key, standardBlockSize, func(ctx context.Context, _ *Progress) Result {
				started <- key
				select {
				case <-release:
				case <-ctx.Done():
				}
				return Result{}
			})
			demands = append(demands, j.Add(need(key, false, time.Second, time.Hour)))
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(started) != 2 {
			t.Fatal("initial ramp", len(started))
		}
		for _, d := range demands {
			d.Update(need("later", false, time.Minute, time.Hour))
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if len(started) != 2 {
			t.Fatal("growth continued without eligible backlog")
		}
		s.call(func(st *state) {
			if st.control.open != 2 || !st.control.nextGrow.IsZero() {
				t.Error("growth timer retained without window backlog")
			}
		})
		// Moving waiting work back into the window starts a fresh growth delay.
		for _, d := range demands[2:] {
			d.Update(need("due", false, time.Second, time.Hour))
		}
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if len(started) != 2 {
			t.Fatal("accumulated growth credit")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if len(started) != 3 {
			t.Fatal("window backlog failed to grow")
		}
		for _, d := range demands {
			d.Update(need("future", false, time.Minute, time.Hour))
		}
		close(release)
		synctest.Wait()
		s.call(func(st *state) {
			if st.active != 0 || st.control.open != 1 || st.ready.Len() != 1 {
				t.Error("future queue prevented idle shrink")
			}
		})
	})
}

func TestSoftLimitWaitHasNoPastWindowWakeup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, GrowInterval: time.Second})
		defer s.Close()
		for _, key := range []string{"running", "waiting"} {
			j, _ := s.Submit(key, standardBlockSize, func(ctx context.Context, _ *Progress) Result { <-ctx.Done(); return Result{} })
			j.Add(need(key, false, -time.Second, time.Hour))
		}
		synctest.Wait()
		s.call(func(st *state) {
			if st.active != 1 || st.ready.Len() != 1 || !st.nextControlEvent(time.Now()).IsZero() {
				t.Error("full quota installed an already elapsed timer")
			}
		})
		time.Sleep(10 * time.Second)
		synctest.Wait()
	})
}

func TestCompletedSlowSampleMovesWindowWithoutReordering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan string, 4)
		now := time.Now()
		probe, _ := s.Submit("probe", standardBlockSize, func(_ context.Context, p *Progress) Result {
			time.Sleep(5 * time.Second)
			p.Ready()
			return Result{}
		})
		probe.Add(need("probe", true, 0, time.Hour))
		for _, key := range []string{"later", "earlier"} {
			j, _ := s.Submit(key, standardBlockSize, func(context.Context, *Progress) Result { started <- key; return Result{} })
			ready := now.Add(40 * time.Second)
			if key == "earlier" {
				ready = now.Add(39 * time.Second)
			}
			j.Add(Need{Owner: key, ReadyBy: ready, LeaseUntil: now.Add(time.Hour)})
		}
		<-probe.Done()
		if got := <-started; got != "earlier" || time.Since(now) != 5*time.Second {
			t.Fatal("sample did not activate earliest deadline", got, time.Since(now))
		}
		if got := <-started; got != "later" || time.Since(now) != 5*time.Second {
			t.Fatal("sample changed deadline order", got)
		}
	})
}

func TestUntimedWorkUsesSpareCapacityAndDeadlineUpdatesMoveHeaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan string, 8)
		gate := make(chan struct{})
		submit := func(key string, ready time.Duration) *Demand {
			j, _ := s.Submit(key, 1, func(ctx context.Context, _ *Progress) Result {
				started <- key
				select {
				case <-gate:
				case <-ctx.Done():
				}
				return Result{}
			})
			return j.Add(need(key, false, ready, time.Hour))
		}
		submit("future", time.Hour/2)
		submit("background", 0)
		if got := <-started; got != "background" {
			t.Fatal("future head blocked untimed work", got)
		}
		changed := submit("changed", 0)
		changed.Update(need("changed", false, time.Second, time.Hour))
		changed.Update(need("changed", false, 0, time.Hour))
		changed.Update(need("changed", false, time.Second, time.Hour))
		submit("last", 0)
		gate <- struct{}{}
		if got := <-started; got != "changed" {
			t.Fatal("eligible deadline lost to background", got)
		}
		gate <- struct{}{}
		if got := <-started; got != "last" {
			t.Fatal("untimed replacement missing", got)
		}
		promoted := submit("promoted", 0)
		promoted.Update(need("promoted", true, 0, time.Hour))
		if got := <-started; got != "promoted" {
			t.Fatal("untimed promotion did not bypass capacity", got)
		}
	})
}

func TestDeadlineUpdatesRearmWindowWithoutTouchingRunningWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 2, GrowInterval: time.Second, PrepareAhead: time.Second})
		defer s.Close()
		started := make(chan string, 2)
		run := func(key string) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				<-ctx.Done()
				return Result{}
			}
		}
		first, _ := s.Submit("first", standardBlockSize, run("first"))
		second, _ := s.Submit("second", standardBlockSize, run("second"))
		a := first.Add(need("first", false, 10*time.Second, time.Hour))
		second.Add(need("second", false, 20*time.Second, time.Hour))
		a.Update(need("first", false, 30*time.Second, time.Hour))
		synctest.Wait()
		s.call(func(st *state) {
			if time.Until(st.nextControlEvent(time.Now())) != 16*time.Second {
				t.Error("deadline change did not move the timed heap head")
			}
		})
		if got := <-started; got != "second" {
			t.Fatal("obsolete window fired", got)
		}
		// The running block's overdue deadline must not trigger capacity growth.
		time.Sleep(5 * time.Second)
		synctest.Wait()
		s.call(func(st *state) {
			if st.control.open != 1 || !st.control.nextGrow.IsZero() {
				t.Error("running work used a full T to request another slot")
			}
		})
		a.Update(need("first", false, time.Second, time.Hour))
		now := time.Now()
		if got := <-started; got != "first" || time.Since(now) != time.Second {
			t.Fatal("earlier deadline failed to enter paced admission", got)
		}
	})
}

func TestLargePrepareAheadStillRampsAndCapsOrdinaryWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 4, GrowInterval: 250 * time.Millisecond, PrepareAhead: 500 * time.Second})
		defer s.Close()
		started := make(chan time.Time, 16)
		run := func(ctx context.Context, _ *Progress) Result { started <- time.Now(); <-ctx.Done(); return Result{} }
		now := time.Now()
		for i := range 10 {
			key := string(rune('a' + i))
			j, _ := s.Submit(key, standardBlockSize, run)
			j.Add(need(key, false, 450*time.Second, time.Hour))
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(started) != 4 {
			t.Fatal("large M bypassed soft limit", len(started))
		}
		for i := range 4 {
			if at := <-started; at.Sub(now) != time.Duration(i)*250*time.Millisecond {
				t.Fatal("large M bypassed ramp", at.Sub(now))
			}
		}
		j, _ := s.Submit("blocking", standardBlockSize, run)
		j.Add(need("reader", true, 900*time.Second, time.Hour))
		if at := <-started; at != time.Now() {
			t.Fatal("Blocking waited for window or quota")
		}
		synctest.Wait()
		s.call(func(st *state) {
			if st.active != 5 || st.control.open != 4 || !st.nextControlEvent(time.Now()).IsZero() {
				t.Error("large window altered active accounting or left a past-time wakeup")
			}
		})
	})
}
