package blocksched

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestMetadataCapacity(t *testing.T) {
	s := New(Config{SoftLimit: 1, MaxTasks: 1, MaxDemands: 1})
	defer s.Close()
	task, d, _ := s.Schedule("one", 1, func(ctx context.Context, _ *Progress) Result {
		<-ctx.Done()
		return Result{Err: context.Canceled}
	}, need(0, time.Hour))
	if _, _, err := s.Schedule("two", 1, nil, need(time.Hour, 2*time.Hour)); err != ErrCapacity {
		t.Fatal("task bound ignored", err)
	}

	if !d.Accepted() || task.Add(need(0, time.Hour)).Accepted() {
		t.Fatal("demand bound ignored")
	}
	if !d.Update(need(time.Minute, time.Hour)) {
		t.Fatal("existing demand update rejected at capacity")
	}
	if task.Add(need(0, time.Hour)).Accepted() {
		t.Fatal("update escaped demand limit")
	}
	d.Close()
	if task.Add(need(0, time.Hour)).Accepted() {
		t.Fatal("withdrawn task accepted a replacement demand")
	}
	task.Close()
	<-task.Done()
	if _, _, err := s.Schedule("two", 1, nil, need(time.Hour, 2*time.Hour)); err != nil {
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
				_, demand, err := s.Schedule(strconv.Itoa(i), 1<<20, func(ctx context.Context, _ *Progress) Result { <-ctx.Done(); return Result{} }, need(time.Minute, time.Hour))
				if err != nil {
					b.Fatal(err)
				}
				d = demand
			}
			n := need(time.Minute, time.Hour)
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
