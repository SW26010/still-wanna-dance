package blocksched

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestLastDemandExitEndsTaskWithoutReattachmentPeriod(t *testing.T) {
	for _, running := range []bool{false, true} {
		for _, expires := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				s := New(Config{SoftLimit: 1, MaxTasks: 1})
				defer s.Close()
				ready, lease := time.Hour, 2*time.Hour
				if running {
					ready = 0
				}
				if expires {
					lease = time.Second
				}
				started := make(chan struct{}, 1)
				task, d, err := s.Schedule("old", 100, func(ctx context.Context, _ *Progress) Result {
					started <- struct{}{}
					<-ctx.Done()
					return Result{Err: context.Cause(ctx)}
				}, need("owner", false, ready, lease))
				if err != nil {
					t.Fatal(err)
				}
				wake := make(chan struct{}, 1)
				task.Watch(wake)
				if running {
					<-started
				}
				if expires {
					time.Sleep(time.Second)
				} else {
					d.Close()
				}
				synctest.Wait()
				select {
				case <-task.Done():
				default:
					t.Fatal("withdrawn task retained a reattachment period", running, expires)
				}
				if !errors.Is(task.Err(), context.Canceled) || len(wake) == 0 || len(started) != 0 {
					t.Fatal("withdrawal did not terminate and notify exactly once", task.Err())
				}
				s.call(func(st *state) {
					if len(st.jobs) != 0 || st.demandCount != 0 || st.active != 0 || len(st.expiry) != 0 || st.ready.Len()+st.untimed.Len() != 0 || !st.nextControlEvent(time.Now()).IsZero() {
						t.Error("stopped task retained capacity, queue membership or timer")
					}
				})
				if task.Add(need("late", true, 0, time.Hour)).Accepted() || d.Update(need("late", true, 0, time.Hour)) {
					t.Fatal("late demand revived stopped task")
				}
				replacement, _, err := s.Schedule("old", 100, nil, need("new", false, time.Hour, 2*time.Hour))
				if err != nil || replacement == task {
					t.Fatal("new task did not get a fresh identity", err)
				}
				task.Close()
				select {
				case <-replacement.Done():
					t.Fatal("old Close affected replacement")
				default:
				}
			})
		}
	}
}

func TestStoppedTaskHoldsCapacityUntilCleanupAndPreservesErrors(t *testing.T) {
	for _, cleanupErr := range []error{nil, context.Canceled, errors.New("block write failed")} {
		synctest.Test(t, func(t *testing.T) {
			s := New(Config{SoftLimit: 1, MaxTasks: 1})
			defer s.Close()
			cleanup, started := make(chan struct{}), make(chan struct{})
			task, d, err := s.Schedule("old", 100, func(ctx context.Context, _ *Progress) Result {
				close(started)
				<-ctx.Done()
				<-cleanup
				return Result{Err: cleanupErr}
			}, need("owner", true, 0, time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			<-started
			d.Close()
			synctest.Wait()
			select {
			case <-task.Done():
				t.Error("Done closed before cleanup")
			default:
			}
			if task.Add(need("late", true, 0, time.Hour)).Accepted() {
				t.Error("stopping task accepted new demand")
			}
			for _, key := range []string{"old", "new"} {
				if _, _, err := s.Schedule(key, 1, nil, need("new", false, time.Hour, 2*time.Hour)); !errors.Is(err, ErrCapacity) {
					t.Error("cleanup released identity or task capacity", key, err)
				}
			}
			s.call(func(st *state) {
				if st.active != 1 || st.demandCount != 0 || len(st.expiry) != 0 || !st.nextControlEvent(time.Now()).IsZero() {
					t.Error("cleanup lost its slot or retained a stale demand/timer")
				}
			})
			close(cleanup)
			want := cleanupErr
			if want == nil {
				want = context.Canceled
			}
			if !errors.Is(task.Err(), want) {
				t.Fatal("cleanup error was hidden", task.Err(), want)
			}
			if _, _, err := s.Schedule("new", 1, nil, need("new", false, time.Hour, 2*time.Hour)); err != nil {
				t.Fatal("cleanup completion leaked capacity", err)
			}
		})
	}
}

func TestScheduleRejectsMissingOrExpiredInitialDemand(t *testing.T) {
	s := New(Config{SoftLimit: 1})
	defer s.Close()
	for _, n := range []Need{{}, {Owner: "expired", LeaseUntil: time.Now()}} {
		if _, _, err := s.Schedule("empty", 1, nil, n); !errors.Is(err, ErrCapacity) {
			t.Fatal("registered a task without valid demand", err)
		}
	}
	s.call(func(st *state) {
		if len(st.jobs) != 0 || st.demandCount != 0 {
			t.Error("invalid registration retained metadata")
		}
	})
}
