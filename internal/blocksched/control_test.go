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
			j, _ := s.Submit(string(rune('a'+i)), 100, func(ctx context.Context, p *Progress) Result {
				started <- time.Now()
				select {
				case <-gates[i]:
					p.Add(100)
				case <-ctx.Done():
				}
				return Result{}
			})
			j.Add(need("same", false, time.Minute, time.Hour))
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
			if s.control.bytes != 0 || s.control.workerSeconds != 0 {
				t.Error("idle carried a drained sample into next burst")
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
		background, _ := s.Submit("background", 1<<20, run)
		background.Add(need("background", false, time.Minute, 100*time.Millisecond))
		<-started
		blocking, _ := s.Submit("blocking", 1<<20, run)
		blocking.Add(need("blocking", true, 0, 100*time.Millisecond))
		<-started
		synctest.Wait()
		var next time.Time
		// Expiry withdraws both contributions in the same event-loop iteration.
		time.Sleep(100 * time.Millisecond)
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

func TestDeadlineCostEstimateAndNoGainRollback(t *testing.T) {
	now := time.Now()
	s := &state{cfg: Config{SoftLimit: 6, Adaptive: true}, jobs: map[string]*job{}, control: controller{rate: 1 << 20}}
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		s.jobs[key] = &job{remaining: 2 << 20, need: Need{Owner: key, ReadyBy: now.Add(2 * time.Second)}}
	}
	if n := s.estimate(now); n != 6 {
		t.Fatal(n)
	}
	s.jobs["a"].need.Blocking = true
	if n := s.estimate(now); n != 6 {
		t.Fatal(n)
	}
	s.control.previousConcurrency = 2
	s.control.previousRate = 2 << 20
	s.control.workerSeconds = 2
	s.control.bytes = 1 << 20
	s.measure(now)
	if s.control.cap != 2 || !s.control.coolUntil.After(now) {
		t.Fatal("no-gain expansion did not back off")
	}
	if n := s.estimate(now); n != 2 {
		t.Fatal(n)
	}
}

func TestEstimateUsesActualTimeUntilDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deadline  time.Duration
		remaining int64
		limit     int
		want      int
	}{
		{"one_second", time.Second, 512 << 10, 16, 4},
		{"half_second", 500 * time.Millisecond, 512 << 10, 16, 8},
		{"round_up", 1500 * time.Millisecond, 512 << 10, 16, 3},
		{"at_deadline", 0, 512 << 10, 16, 2},
		{"overdue", -time.Second, 512 << 10, 16, 2},
		{"future_soft_limit", time.Second, 512 << 10, 3, 3},
		{"near_deadline_large_backlog", time.Nanosecond, 1 << 51, 16, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			s := &state{cfg: Config{SoftLimit: tc.limit, Adaptive: true}, jobs: make(map[string]*job), control: controller{rate: 1 << 20}}
			for i := range 8 {
				key := string(rune('a' + i))
				s.jobs[key] = &job{remaining: tc.remaining, need: Need{Owner: key, ReadyBy: now.Add(tc.deadline)}}
			}
			if got := s.estimate(now); got != tc.want {
				t.Fatalf("estimated %d requests, want %d", got, tc.want)
			}
		})
	}
}
