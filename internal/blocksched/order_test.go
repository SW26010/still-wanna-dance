package blocksched

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestOpportunisticOrderConcentratesInterleavedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan string, 32)
		s := New(Config{SoftLimit: 2, Observe: func(e Event) {
			if e.Kind == "started" {
				started <- e.Key
			}
		}})
		defer s.Close()
		held := make(chan struct{})
		run := func(ctx context.Context, _ *Progress) Result {
			select {
			case <-held:
			case <-ctx.Done():
			}
			return Result{}
		}
		for i := range 2 {
			_, _, err := s.Schedule(fmt.Sprint("hold", i), 1, run, need("hold", 0, time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			<-started
		}
		// Resource coordinators register independently, interleaving their blocks.
		for block := range 4 {
			for order := uint64(1); order <= 3; order++ {
				n := need(fmt.Sprint(order), 0, time.Hour)
				n.OpportunisticOrder = order
				if _, _, err := s.Schedule(fmt.Sprintf("%d/%d", order, block), 1, run, n); err != nil {
					t.Fatal(err)
				}
			}
		}
		close(held)
		for order := uint64(1); order <= 3; order++ {
			for block := range 4 {
				if got, want := <-started, fmt.Sprintf("%d/%d", order, block); got != want {
					t.Fatalf("scattered unfinished work: started %s, want %s", got, want)
				}
			}
		}
	})
}

func TestOpportunisticOrderPreservesFinitePriorityAndMixedSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan string, 8)
		gate := make(chan struct{})
		run := func(key string) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				select {
				case <-gate:
				case <-ctx.Done():
				}
				return Result{}
			}
		}
		_, _, _ = s.Schedule("hold", 1, run("hold"), need("hold", 0, time.Hour))
		<-started
		for _, key := range []string{"second", "first", "default", "due"} {
			n := need(key, 0, time.Hour)
			switch key {
			case "first":
				n.OpportunisticOrder = 1
			case "second":
				n.OpportunisticOrder = 2
			case "due":
				n.ReadyBy = time.Now().Add(time.Second)
			}
			j, _, err := s.Schedule(key, 1, run(key), n)
			if err != nil {
				t.Fatal(err)
			}
			if key == "second" || key == "first" {
				// Future finite work must neither erase the independent order nor
				// use its deadline to jump ahead in the early-admission heap.
				future := 10 * time.Minute
				if key == "first" {
					future = 20 * time.Minute
				}
				j.Add(need(key, future, time.Hour))
			}
		}
		for _, want := range []string{"due", "default", "first", "second"} {
			gate <- struct{}{}
			if got := <-started; got != want {
				t.Fatalf("started %s, want %s", got, want)
			}
		}
		close(gate)
	})
}

func TestOpportunisticOrderFollowsLiveContributions(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(Config{SoftLimit: 1})
				defer s.Close()
				_, _, _ = s.Schedule("hold", 1, func(ctx context.Context, _ *Progress) Result {
					<-ctx.Done()
					return Result{}
				}, need("hold", 0, time.Hour))
				j, _, _ := s.Schedule("shared", 1, nil, need("v", 20*time.Minute, time.Hour))
				a := need("v", 0, time.Second)
				a.OpportunisticOrder = 1
				first := j.Add(a)
				b := need("v", 0, time.Hour)
				b.OpportunisticOrder = 2
				second := j.Add(b)
				check := func(order uint64, early bool) {
					t.Helper()
					s.call(func(st *state) {
						n := st.jobs["shared"].need
						if n.OpportunisticOrder != order || n.Opportunistic != early || n.ReadyBy.IsZero() {
							t.Errorf("stale shared contribution: %+v, want order=%d early=%v", n, order, early)
						}
					})
				}
				check(1, true)
				if expire {
					time.Sleep(time.Second)
				} else {
					first.Close()
				}
				check(2, true)
				b.WindowClosed = true
				second.Update(b)
				check(0, false)
				b.WindowClosed = false
				second.Update(b)
				unordered := j.Add(need("v", 0, time.Hour))
				check(0, true)
				unordered.Close()
				check(2, true)
				second.Close()
				check(0, false)
			})
		})
	}
}

func TestOpportunisticOrderDoesNotDelayBlockedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan struct{})
		_, _, _ = s.Schedule("hold", 1, func(ctx context.Context, _ *Progress) Result {
			close(started)
			<-ctx.Done()
			return Result{}
		}, need("hold", 0, time.Hour))
		<-started
		reads := s.NewReadRequests("late", func(int64) string { return "late" }, nil)
		defer reads.Close()
		end := reads.Wait(0)
		defer end()
		n := need("late", 0, time.Hour)
		n.OpportunisticOrder = 99
		j, _, err := s.Schedule("late", 1, func(context.Context, *Progress) Result { return Result{} }, n)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-j.Done():
		default:
			t.Fatal("numbered work blocked an actual read behind the soft limit")
		}
	})
}

func TestOpportunisticOrderingIsTransitive(t *testing.T) {
	var jobs []*job
	for order := uint64(0); order <= 2; order++ {
		for deadline := range 3 {
			jobs = append(jobs, &job{need: priority{Need: Need{
				OpportunisticOrder: order, ReadyBy: time.Unix(int64(deadline), 0),
			}}, fair: uint64(3 - deadline), sequence: uint64(len(jobs))})
		}
	}
	h := readyHeap{jobs: jobs, slot: 1}
	for a := range jobs {
		for b := range jobs {
			for c := range jobs {
				if h.Less(a, b) && h.Less(b, c) && !h.Less(a, c) {
					t.Fatalf("nontransitive early order: %d < %d < %d", a, b, c)
				}
			}
		}
	}
}
