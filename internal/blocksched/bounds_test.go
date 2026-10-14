package blocksched

import (
	"context"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func TestSummaryUpdatesReleaseOldOwnersAndCounters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{Limit: 1, Background: 1})
		defer s.Close()
		j, _ := s.Submit("shared", 100, func(ctx context.Context, _ *Progress) Result {
			<-ctx.Done()
			return Result{Err: ErrPaused, Remaining: 100}
		})
		d := j.Add(need("first", true, 0, time.Hour))
		for i := 0; i < 100; i++ {
			d.Update(need(strconv.Itoa(i), i%2 == 0, time.Minute, time.Hour))
		}
		s.call(func(st *state) {
			if st.wanted != 1 || st.blocking != 0 || len(st.groups) != 1 || len(st.owners) > 1 {
				t.Errorf("stale owner/counters: %d %d %d %d", st.wanted, st.blocking, len(st.groups), len(st.owners))
			}
		})
		d.Close()
		synctest.Wait()
		s.call(func(st *state) {
			if st.wanted != 0 || st.blocking != 0 || len(st.groups) != 0 || len(st.sleeping) != 0 {
				t.Error("withdrawn demand retained scheduler metadata")
			}
		})
		j.Close()
	})
}

func TestRetriedBytesDoNotInflateSupply(t *testing.T) {
	now := time.Now()
	p := new(Progress)
	j := &job{progress: p, started: now.Add(-sampleInterval), remaining: 100, initialRemaining: 100}
	s := &state{running: map[*job]bool{j: true}, control: controller{rate: 42}}
	p.Add(1000)
	p.Invalidate()
	s.measure(now)
	if s.control.rate != 42 || j.remaining != 100 {
		t.Fatal("retry inflated throughput or erased remaining work")
	}
}

func TestResponseCostDiscoveryAndMetadataCapacity(t *testing.T) {
	p := new(Progress)
	p.SetSize(8 << 20)
	p.Add(1 << 20)
	j := &job{progress: p, initialRemaining: 1 << 20, remaining: 1 << 20, started: time.Now()}
	st := &state{}
	st.sampleProgress(j, time.Now())
	if j.remaining != 7<<20 {
		t.Fatal("sequential fallback cost was truncated", j.remaining)
	}
	s := New(Config{Limit: 1, Background: 1, MaxTasks: 1, MaxDemands: 1})
	defer s.Close()
	task, _ := s.Submit("one", 1, func(ctx context.Context, _ *Progress) Result {
		<-ctx.Done()
		return Result{Err: ErrPaused, Remaining: 1}
	})
	if _, err := s.Submit("two", 1, nil); err != ErrCapacity {
		t.Fatal("task bound ignored", err)
	}
	d := task.Add(need("one", true, 0, time.Hour))
	if !d.Accepted() || task.Add(need("two", false, 0, time.Hour)).Accepted() {
		t.Fatal("demand bound ignored")
	}
	if !d.Update(need("one", false, time.Minute, time.Hour)) {
		t.Fatal("existing demand update rejected at capacity")
	}
	if task.Add(need("two", false, 0, time.Hour)).Accepted() {
		t.Fatal("update escaped demand limit")
	}
	d.Close()
	if !task.Add(need("two", false, 0, time.Hour)).Accepted() {
		t.Fatal("closed demand leaked capacity")
	}
	task.Close()
	<-task.Done()
	if _, err := s.Submit("two", 1, nil); err != nil {
		t.Fatal("completed task leaked capacity", err)
	}
}

func BenchmarkDemandUpdate(b *testing.B) {
	for _, count := range []int{16, 1024, 4096} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			s := New(Config{Limit: 1, Background: 1, GrowInterval: 250 * time.Millisecond, Adaptive: true})
			defer s.Close()
			var d *Demand
			for i := 0; i < count; i++ {
				j, err := s.Submit(strconv.Itoa(i), 1<<20, func(ctx context.Context, _ *Progress) Result { <-ctx.Done(); return Result{} })
				if err != nil {
					b.Fatal(err)
				}
				d = j.Add(need(strconv.Itoa(i), false, time.Minute, time.Hour))
			}
			n := need("update", false, time.Minute, time.Hour)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n.ReadyBy = n.ReadyBy.Add(time.Nanosecond)
				d.Update(n)
			}
			b.StopTimer()
		})
	}
}
