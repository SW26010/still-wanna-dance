package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestRampReuseAndNoIdleCredit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 6, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		started := make(chan time.Time, 8)
		gates := make([]chan struct{}, 4)
		for i := range gates {
			gates[i] = make(chan struct{})
			_, _, _ = s.Schedule(string(rune('a'+i)), 100, func(ctx context.Context, p *Progress) Result {
				started <- time.Now()
				select {
				case <-gates[i]:
				case <-ctx.Done():
				}
				return Result{}
			}, need("same", time.Second, time.Hour))

		}
		first := <-started
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("unbounded startup burst")
		default:
		}
		second := <-started
		if second.Sub(first) != 250*time.Millisecond {
			t.Fatal(second.Sub(first))
		}
		close(gates[0])
		third := <-started
		if !third.Equal(second) {
			t.Fatal("existing capacity was ramped again")
		}
		for _, g := range gates[1:] {
			close(g)
		}
		synctest.Wait()
		time.Sleep(time.Minute)
		var open int
		s.call(func(s *state) {
			open = s.control.open
			if !s.nextControlEvent(time.Now()).IsZero() {
				t.Error("idle scheduler has a control timer")
			}
		})
		if open != 1 {
			t.Fatalf("idle capacity accumulated: %d", open)
		}
	})
}

func TestIdleStopsControlTimersWhenAllLeasesExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		started := make(chan struct{}, 2)
		run := func(ctx context.Context, _ *Progress) Result {
			started <- struct{}{}
			<-ctx.Done()
			return Result{}
		}
		_, _, _ = s.Schedule("background", 1<<20, run, need("background", time.Second, 100*time.Millisecond))

		<-started
		endRead := waitForKey(t, s, "blocking")
		_, _, _ = s.Schedule("blocking", 1<<20, run, need("blocking", 0, 100*time.Millisecond))

		<-started
		synctest.Wait()
		var next time.Time
		// Expiry withdraws both contributions in the same event-loop iteration.
		time.Sleep(100 * time.Millisecond)
		endRead()
		synctest.Wait()
		s.call(func(s *state) { next = s.nextControlEvent(time.Now()) })
		if !next.IsZero() {
			t.Fatalf("idle scheduler still has a control wakeup in %v", time.Until(next))
		}
		// No recurring control wakeup remains once all work has finished.
		time.Sleep(time.Second)
		synctest.Wait()
	})
}
