package blocksched

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func need(ready, lease time.Duration) Need {
	n := Need{LeaseUntil: time.Now().Add(lease)}
	if ready != 0 {
		n.ReadyBy = time.Now().Add(ready)
	}
	return n
}

func TestIndependentLeasesAndLateUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan struct{}, 2)
		j, a, _ := s.Schedule("shared", 100, func(ctx context.Context, p *Progress) Result {
			started <- struct{}{}
			<-ctx.Done()
			return Result{Err: context.Canceled}
		}, need(-time.Second, time.Second))

		later := need(time.Minute, 3*time.Second)
		b := j.Add(later)
		<-started
		time.Sleep(2 * time.Second)
		if a.Update(need(0, time.Hour)) {
			t.Fatal("expired demand revived")
		}
		var summary priority
		s.call(func(s *state) { summary = s.jobs["shared"].need })
		if summary.Blocking || summary.ReadyBy != later.ReadyBy {
			t.Fatalf("stale urgency: %+v", summary)
		}
		b.Close()
		if b.Update(need(0, time.Hour)) {
			t.Fatal("closed handle revived demand")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		var active int
		s.call(func(s *state) { active = s.active })
		if active != 0 {
			t.Fatalf("orphan still active: %d", active)
		}
		j.Close()
	})
}

func TestSharedExecutionEqualDeadlinesAndReleaseAfterCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		gate := make(chan struct{})
		cleanup := make(chan struct{})
		started := make(chan string, 8)
		_, d, _ := s.Schedule("held", 1, func(ctx context.Context, p *Progress) Result {
			started <- "held"
			<-ctx.Done()
			<-cleanup
			return Result{Err: context.Canceled}
		}, need(0, time.Hour))

		<-started
		keys := []string{"a1", "a2", "b1"}
		for _, key := range keys {
			j, _, _ := s.Schedule(key, 1, func(ctx context.Context, p *Progress) Result { started <- key; <-gate; return Result{} }, need(time.Minute, time.Hour))

			dup, _, _ := s.Schedule(key, 1, nil, need(time.Minute, time.Hour))
			if dup != j {
				t.Fatal("duplicate execution")
			}
		}
		d.Close()
		synctest.Wait()
		select {
		case <-started:
			t.Fatal("released before cleanup")
		default:
		}
		close(cleanup)
		defer close(gate)
		for _, want := range keys {
			if got := <-started; got != want {
				t.Fatalf("started %s, want %s", got, want)
			}
			gate <- struct{}{}
		}
	})
}

func TestDeadlineDoesNotCancelAndCloseDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 1})
		j, _, _ := s.Schedule("a", 100, func(ctx context.Context, p *Progress) Result {
			<-ctx.Done()
			return Result{Err: context.Canceled}
		}, need(time.Second, time.Minute))

		time.Sleep(2 * time.Second)
		select {
		case <-j.Done():
			t.Fatal("ReadyBy canceled task")
		default:
		}
		if _, _, err := s.Schedule("b", 1, nil, need(time.Hour, 2*time.Hour)); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		s.Close()
		if !errors.Is(j.Err(), ErrClosed) {
			t.Fatal(j.Err())
		}
		if _, _, err := s.Schedule("c", 1, nil, need(time.Hour, 2*time.Hour)); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	})
}
