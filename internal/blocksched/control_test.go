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

func TestContentionPausesProgressWithoutCancelStorm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 3, GrowInterval: 250 * time.Millisecond, PreemptAfter: 200 * time.Millisecond, Cooldown: 500 * time.Millisecond})
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

func TestContentionStopsWhenAllLeasesExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond, PreemptAfter: 200 * time.Millisecond})
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
		var contention, next time.Time
		s.call(func(s *state) { contention = s.contention })
		if contention.IsZero() {
			t.Fatal("test did not establish contention")
		}
		// Expiry withdraws both contributions in the same event-loop iteration.
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		s.call(func(s *state) { next = s.nextControlEvent(time.Now()) })
		if !next.IsZero() {
			t.Fatalf("idle scheduler still has a control wakeup in %v", time.Until(next))
		}
		// Crossing the former contention deadline must leave the loop idle.
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

func TestDueDeadlineCanYieldSmallRequestWithoutProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, PreemptAfter: 200 * time.Millisecond, Cooldown: time.Second})
		defer s.Close()
		started := make(chan string, 4)
		old, _ := s.Submit("headers", 25, func(ctx context.Context, _ *Progress) Result {
			started <- "old"
			<-ctx.Done()
			return Result{Err: ErrPaused, Remaining: 25}
		})
		old.Add(need("old", false, time.Minute, time.Hour))
		<-started
		now := time.Now()
		next, _ := s.Submit("due", 25, func(ctx context.Context, _ *Progress) Result { started <- "due"; return Result{} })
		next.Add(need("next", false, time.Second, time.Hour))
		if got := <-started; got != "due" || time.Since(now) != 200*time.Millisecond {
			t.Fatal("deadline stuck behind silent small request", got, time.Since(now))
		}
		old.Close()
	})
}

func TestContentionUsesCurrentProgressBeforeNextSample(t *testing.T) {
	for _, tc := range []struct {
		name           string
		size, received int64
		retry, pause   bool
	}{
		{"small_tail", 1 << 20, (1 << 20) - (32 << 10), false, false},
		{"awaiting_eof", 1 << 20, 1 << 20, false, false},
		{"large_tail", 1 << 20, (1 << 20) - (128 << 10), false, true},
		{"sequential_cost", 8 << 20, 1 << 20, false, true},
		{"retry_tail", 1 << 20, (1 << 20) - (32 << 10), true, false},
		{"retry_headers", 32 << 10, 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			p := new(Progress)
			paused := false
			j := &job{progress: p, remaining: 1 << 20, initialRemaining: 1 << 20, started: now,
				need: Need{Owner: "background", ReadyBy: now.Add(time.Minute)}, cancel: func(error) { paused = true }}
			s := &state{cfg: Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond, PreemptAfter: 200 * time.Millisecond},
				running: map[*job]bool{j: true}, jobs: map[string]*job{"background": j}, active: 1, wanted: 2, blocking: 1}
			s.regulate(now) // The next throughput sample is at 500 ms.
			p.SetSize(tc.size)
			if tc.retry {
				p.Add(tc.size)
				p.Invalidate()
			}
			p.Add(tc.received)
			s.regulate(now.Add(200 * time.Millisecond))
			if paused != tc.pause {
				t.Fatalf("paused=%v, want %v; sampled remaining=%d", paused, tc.pause, j.remaining)
			}
			if !s.control.nextSample.Equal(now.Add(sampleInterval)) {
				t.Fatal("victim selection changed throughput sampling cadence")
			}
		})
	}
}
