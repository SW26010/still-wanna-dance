package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestRampReuseEmergencyAndNoIdleCredit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{Limit: 6, Background: 4, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		started := make(chan time.Time, 8)
		gates := make([]chan struct{}, 4)
		for i := range gates {
			gates[i] = make(chan struct{})
			j, _ := s.Submit(string(rune('a'+i)), 100, func(ctx context.Context, p *Progress) Result {
				started <- time.Now()
				select {
				case <-gates[i]:
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
		s.call(func(s *state) { open = s.control.open })
		if open != 1 {
			t.Fatalf("idle capacity accumulated: %d", open)
		}
	})
}

func TestContentionPausesProgressWithoutCancelStorm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{Limit: 3, Background: 2, GrowInterval: 250 * time.Millisecond, PreemptAfter: 200 * time.Millisecond, Cooldown: 500 * time.Millisecond})
		defer s.Close()
		paused := make(chan time.Time, 1)
		started := make(chan struct{}, 4)
		release := make(chan struct{})
		j, _ := s.Submit("background", 1<<20, func(ctx context.Context, p *Progress) Result {
			started <- struct{}{}
			<-ctx.Done()
			paused <- time.Now()
			return Result{Err: ErrPaused, Remaining: 512 << 10}
		})
		j.Add(need("bg", false, time.Minute, time.Hour))
		<-started
		front, _ := s.Submit("read", 1<<20, func(ctx context.Context, p *Progress) Result {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return Result{}
		})
		now := time.Now()
		front.Add(need("read", true, 0, time.Hour))
		if delay := (<-paused).Sub(now); delay != 200*time.Millisecond {
			t.Fatal(delay)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("paused request restarted against blocked read")
		default:
		}
		close(release)
		<-started
		j.Close()
	})
}

func TestDeadlineCostEstimateAndNoGainRollback(t *testing.T) {
	now := time.Now()
	s := &state{cfg: Config{Limit: 6, Background: 4, Adaptive: true}, jobs: map[string]*job{}, control: controller{rate: 1 << 20}}
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		s.jobs[key] = &job{remaining: 2 << 20, need: Need{Owner: key, ReadyBy: now.Add(2 * time.Second)}}
	}
	if n := s.estimate(now); n != 4 {
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
