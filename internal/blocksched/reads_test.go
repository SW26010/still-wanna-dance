package blocksched

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func waitForKey(t *testing.T, s *Scheduler, key string) func() {
	t.Helper()
	r := s.NewReadRequests(key, func(int64) string { return key }, nil)
	end := r.Wait(0)
	t.Cleanup(r.Close)
	return end
}

func TestReadWaitsBeforeRegistrationAndIndependentWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, GrowInterval: time.Hour})
		defer s.Close()
		started := make(chan string, 3)
		run := func(key string) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				<-ctx.Done()
				return Result{Err: ctx.Err()}
			}
		}
		s.Schedule("held", 1, run("held"), need("held", 0, time.Hour))
		<-started
		r := s.NewReadRequests("video", func(int64) string { return "shared" }, nil)
		defer r.Close()
		one, two := r.Wait(7), r.Wait(7)
		if len(r.Requested()) != 1 || !r.Requested()[7] {
			t.Fatal("read before task registration was lost")
		}
		task, ordinary, err := s.Schedule("shared", 1, run("shared"), need("video", time.Hour, 2*time.Hour))
		if err != nil || <-started != "shared" {
			t.Fatal("missing read did not bypass capacity/window", err)
		}
		assertBlocking := func(want bool) {
			t.Helper()
			s.call(func(st *state) {
				j := st.jobs["shared"]
				if j == nil || j.need.Blocking != want || j.retiring || j.need.ReadyBy != time.Now().Add(time.Hour) {
					t.Error("read cleanup lost a shared execution", want)
				}
			})
		}
		assertBlocking(true)
		one()
		one()
		assertBlocking(true)
		two()
		assertBlocking(false)
		if len(r.Requested()) != 0 {
			t.Fatal("ended reads retained metadata")
		}
		// Removing urgency keeps the original clock and the same execution.
		s.call(func(st *state) {
			if st.jobs["shared"].need.ReadyBy != time.Now().Add(time.Hour) || st.active != 2 {
				t.Error("read urgency contaminated ordinary usage or active accounting")
			}
		})
		ordinary.Close()
		if !errors.Is(task.Err(), context.Canceled) {
			t.Fatal("last contribution did not end execution")
		}
	})
}

func TestReadOnlyTaskSurvivesUsageWithdrawalAndStopsWithLastRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		r := s.NewReadRequests("video", func(int64) string { return "block" }, nil)
		end := r.Wait(0)
		task, ordinary, err := s.Schedule("block", 1, func(ctx context.Context, _ *Progress) Result {
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, need("video", time.Minute, time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ordinary.Close()
		s.call(func(st *state) {
			if j := st.jobs["block"]; j == nil || j.retiring || !j.need.Blocking || len(j.needs) != 0 {
				t.Error("usage withdrawal canceled an independent read")
			}
		})
		end()
		if !errors.Is(task.Err(), context.Canceled) {
			t.Fatal("read-only task survived its last reader")
		}
		// Session close removes waits without a task and rejects old readers.
		r.Wait(3)
		r.Close()
		r.Wait(4)()
		if len(r.Requested()) != 0 {
			t.Fatal("closed session acquired a new read")
		}
		s.call(func(st *state) {
			if len(st.reads) != 0 {
				t.Error("session close leaked wait registry")
			}
		})
	})
}

func TestReadWaitDuringRetirementUsesFreshTaskAfterCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 1})
		defer s.Close()
		started, cleanup := make(chan struct{}), make(chan struct{})
		old, demand, err := s.Schedule("block", 1, func(ctx context.Context, _ *Progress) Result {
			close(started)
			<-ctx.Done()
			<-cleanup
			return Result{Err: ctx.Err()}
		}, need("video", 0, time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		<-started
		demand.Close()
		r := s.NewReadRequests("video", func(int64) string { return "block" }, nil)
		defer r.Close()
		end := r.Wait(0)
		defer end()
		if demand.Update(need("video", 0, time.Hour)) {
			t.Fatal("read revived a retired ordinary demand")
		}
		if _, _, err := s.Schedule("block", 1, nil, Need{}); !errors.Is(err, ErrCapacity) {
			t.Fatal("read overlapped old response cleanup", err)
		}
		close(cleanup)
		<-old.Done()
		next, ordinary, err := s.Schedule("block", 1, func(context.Context, *Progress) Result { return Result{} }, Need{})
		if err != nil || next == old || ordinary != nil {
			t.Fatal("pending read failed to acquire fresh work without a usage lease", err)
		}
		if err := next.Err(); err != nil {
			t.Fatal(err)
		}
		end()
		if len(r.Requested()) != 0 {
			t.Fatal("completed read survived cleanup")
		}
	})
}

func TestReadWaitKeepsSourceDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var events []Event
		s := New(Config{SoftLimit: 1, GrowInterval: time.Hour, Observe: func(e Event) { events = append(events, e) }})
		defer s.Close()
		started := make(chan string, 2)
		run := func(key string) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				<-ctx.Done()
				return Result{Err: ctx.Err()}
			}
		}
		s.Schedule("held", 1, run("held"), need("held", 0, time.Hour))
		<-started
		base := time.Now()
		task, _, err := s.Schedule("block", 1, run("block"), need("video", time.Minute, time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		defer waitForKey(t, s, "block")()
		if key := <-started; key != "block" || time.Since(base) != 5*time.Second {
			t.Fatal("actual read did not bypass the occupied ordinary slot", key)
		}
		if deadline := s.TaskDeadline("block"); !deadline.Equal(base.Add(90 * time.Second)) {
			t.Error("actual read changed the source deadline", deadline.Sub(base))
		}
		if view := s.Observation().Tasks["block"]; !view.WaitSince.Equal(base.Add(5*time.Second)) || !view.Deadline.Equal(base.Add(90*time.Second)) {
			t.Error("observation conflated expected use and actual wait", view)
		}
		for _, e := range events {
			if e.Key == "block" && e.Kind == "started" && (!e.WaitSince.Equal(base.Add(5*time.Second)) || !e.ReadyBy.Equal(base.Add(time.Minute)) || !e.Blocking || e.Overdue) {
				t.Error("event conflated expected use and actual wait", e)
			}
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		select {
		case <-task.Done():
			t.Fatal("actual wait imposed an early 30-second deadline", task.Err())
		default:
		}
		time.Sleep(55 * time.Second)
		if err := task.Err(); !errors.Is(err, ErrExpired) || time.Since(base) != 90*time.Second {
			t.Fatal("source need-by + grace did not end the task", err)
		}
	})
}

func TestPendingReadDoesNotCreateNeedByDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		r := s.NewReadRequests("video", func(int64) string { return "block" }, nil)
		defer r.Close()
		end := r.Wait(0)
		time.Sleep(DownloadGrace)
		task, ordinary, err := s.Schedule("block", 1, func(ctx context.Context, _ *Progress) Result {
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, Need{})
		if err != nil || ordinary != nil {
			t.Fatal("read-only registration invented an expired source", err)
		}
		time.Sleep(DownloadGrace)
		if deadline := s.TaskDeadline("block"); !deadline.IsZero() {
			t.Fatal("read-only work acquired a finite need-by", deadline)
		}
		end()
		if err := task.Err(); !errors.Is(err, context.Canceled) {
			t.Fatal("last actual read did not cancel read-only work", err)
		}
	})
}

func TestReadRegistersBeforeOrdinaryWorkFillsTaskCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 1})
		defer s.Close()
		r := s.NewReadRequests("video", func(block int64) string { return fmt.Sprint(block) }, nil)
		defer r.Close()
		defer r.Wait(1)()
		blocks := []int64{0, 1}
		r.OrderRegistration(blocks)
		started := make(chan int64, 2)
		for _, block := range blocks {
			_, _, err := s.Schedule(r.Key(block), 1, func(ctx context.Context, _ *Progress) Result {
				started <- block
				<-ctx.Done()
				return Result{Err: ctx.Err()}
			}, need("video", 0, time.Hour))
			if block == 1 && err != nil || block == 0 && !errors.Is(err, ErrCapacity) {
				t.Fatal("ordinary registration took the waiting read's task slot", block, err)
			}
		}
		if block := <-started; block != 1 {
			t.Fatal("ordinary execution blocked required read registration", block)
		}
	})
}
