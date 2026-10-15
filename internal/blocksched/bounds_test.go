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
		s := New(Config{SoftLimit: 1})
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
			if len(st.groups) != 1 || len(st.owners) > 1 {
				t.Errorf("stale owner/counters: %d %d", len(st.groups), len(st.owners))
			}
		})
		d.Close()
		synctest.Wait()
		s.call(func(st *state) {
			if len(st.groups) != 0 || len(st.ready)+len(st.untimed) != 0 {
				t.Error("withdrawn demand retained scheduler metadata")
			}
		})
		j.Close()
	})
}

func TestMetadataCapacity(t *testing.T) {
	s := New(Config{SoftLimit: 1, MaxTasks: 1, MaxDemands: 1})
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
			s := New(Config{SoftLimit: 1, GrowInterval: 250 * time.Millisecond})
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
