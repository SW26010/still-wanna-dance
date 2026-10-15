package blocksched

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestOrphanExpiryReclaimsTaskCapacity(t *testing.T) {
	for _, mode := range []string{"expired", "withdrawn", "never_demanded"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(Config{SoftLimit: 1, MaxTasks: 1, GrowInterval: 250 * time.Millisecond})
				defer s.Close()
				started := make(chan struct{}, 1)
				task, _ := s.Submit("old", 100, func(ctx context.Context, _ *Progress) Result {
					started <- struct{}{}
					<-ctx.Done()
					return Result{Err: ErrPaused, Remaining: 80}
				})
				wake := make(chan struct{}, 1)
				task.Watch(wake)
				var demand *Demand
				if mode != "never_demanded" {
					lease := time.Second
					if mode == "withdrawn" {
						lease = time.Hour
					}
					demand = task.Add(need("owner", false, 0, lease))
					<-started
					time.Sleep(time.Second)
					if mode == "withdrawn" {
						demand.Close()
					}
				}
				synctest.Wait()
				time.Sleep(orphanRetention - time.Nanosecond)
				synctest.Wait()
				if _, err := s.Submit("new", 1, nil); !errors.Is(err, ErrCapacity) {
					t.Fatal("retention ended early", err)
				}
				if duplicate, _ := s.Submit("old", 100, nil); duplicate != task {
					t.Fatal("idle task could not be shared during retention")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				select {
				case <-task.Done():
				default:
					t.Fatal("orphan did not finish without Task.Close")
				}
				if !errors.Is(task.Err(), context.Canceled) {
					t.Fatal("orphan reported success", task.Err())
				}
				select {
				case <-wake:
				default:
					t.Fatal("coordinator was not notified of retirement")
				}
				s.call(func(st *state) {
					if len(st.jobs) != 0 || st.demandCount != 0 || st.active != 0 || st.orphans.Len() != 0 || !st.nextControlEvent(time.Now()).IsZero() {
						t.Error("orphan retained task capacity or a timer")
					}
				})
				if task.Add(need("late", true, 0, time.Hour)).Accepted() || demand != nil && demand.Update(need("late", true, 0, time.Hour)) {
					t.Fatal("late demand revived retired task")
				}
				replacement, err := s.Submit("old", 100, nil)
				if err != nil || replacement == task {
					t.Fatal("orphan leaked capacity or identity", err)
				}
				task.Close()
				synctest.Wait()
				select {
				case <-replacement.Done():
					t.Fatal("stale Close retired replacement")
				default:
				}
			})
		})
	}
}

func TestOrphanReattachmentResetsOnlyItsOwnRetention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		started := make(chan struct{}, 1)
		first, _ := s.Submit("first", 100, func(ctx context.Context, _ *Progress) Result {
			started <- struct{}{}
			<-ctx.Done()
			return Result{Err: ErrPaused, Remaining: 80}
		})
		second, _ := s.Submit("second", 100, nil)
		time.Sleep(5 * time.Second)
		demand := first.Add(need("new", false, 0, time.Hour))
		if !demand.Accepted() {
			t.Fatal("reattachment during retention rejected")
		}
		<-started
		// Cross the original retirement time while a new valid demand exists.
		time.Sleep(orphanRetention)
		synctest.Wait()
		select {
		case <-first.Done():
			t.Fatal("old orphan timer canceled reattached task")
		default:
		}
		select {
		case <-second.Done():
		default:
			t.Fatal("reattachment prolonged another orphan")
		}
		demand.Close()
		synctest.Wait()
		time.Sleep(orphanRetention)
		synctest.Wait()
		select {
		case <-first.Done():
		default:
			t.Fatal("second orphan period was never reclaimed")
		}
	})
}

func TestOrphanRetirementWaitsForCleanup(t *testing.T) {
	for _, cleanupErr := range []error{nil, ErrPaused, errors.New("checkpoint write failed")} {
		synctest.Test(t, func(t *testing.T) {
			s := New(Config{SoftLimit: 1, MaxTasks: 1})
			defer s.Close()
			cleanup := make(chan struct{})
			started := make(chan struct{})
			task, _ := s.Submit("old", 100, func(ctx context.Context, _ *Progress) Result {
				close(started)
				<-ctx.Done()
				<-cleanup
				return Result{Err: cleanupErr, Remaining: 80}
			})
			task.Add(need("owner", true, 0, time.Second))
			<-started
			time.Sleep(time.Second + orphanRetention)
			synctest.Wait()
			select {
			case <-task.Done():
				t.Error("retirement finished before executor cleanup")
			default:
			}
			if task.Add(need("late", true, 0, time.Hour)).Accepted() {
				t.Error("retiring executor accepted a new demand")
			}
			if _, err := s.Submit("new", 1, nil); !errors.Is(err, ErrCapacity) {
				t.Error("capacity released before executor cleanup", err)
			}
			s.call(func(st *state) {
				if st.orphans.Len() != 0 || !st.nextControlEvent(time.Now()).IsZero() {
					t.Error("retiring orphan retained an expired timer")
				}
			})
			close(cleanup)
			want := cleanupErr
			if want == nil || want == ErrPaused {
				want = context.Canceled
			}
			if err := task.Err(); !errors.Is(err, want) {
				t.Fatal("retirement lost cleanup result", err)
			}
			if _, err := s.Submit("new", 1, nil); err != nil {
				t.Fatal("completed cleanup did not release capacity", err)
			}
		})
	}
}

func TestOrphanQueuedAndPausedTasksExpireDuringBlocking(t *testing.T) {
	for _, paused := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := New(Config{SoftLimit: 1, MaxTasks: 2})
			defer s.Close()
			started := make(chan struct{}, 1)
			background, _ := s.Submit("background", 1<<20, func(ctx context.Context, _ *Progress) Result {
				started <- struct{}{}
				<-ctx.Done()
				return Result{Err: ErrPaused, Remaining: 512 << 10}
			})
			if paused {
				d := background.Add(need("background", false, time.Second, time.Second))
				<-started
				d.Close()
			}
			blocking, _ := s.Submit("blocking", 1, func(ctx context.Context, _ *Progress) Result {
				<-ctx.Done()
				return Result{}
			})
			blocking.Add(need("reader", true, 0, time.Hour))
			if !paused {
				background.Add(need("background", false, time.Second, time.Second))
			}
			time.Sleep(time.Second + orphanRetention)
			synctest.Wait()
			select {
			case <-background.Done():
			default:
				t.Fatal("Blocking prevented orphan retirement", paused)
			}
			if len(started) != 0 {
				t.Fatal("orphan started or resumed without demand")
			}
			s.call(func(st *state) {
				if len(st.jobs) != 1 || st.demandCount != 1 || st.active != 1 || st.orphans.Len() != 0 || st.ready.Len() != 0 {
					t.Error("orphan left metadata behind or removed the independent reader")
				}
			})
			if _, err := s.Submit("new", 1, nil); err != nil {
				t.Fatal("idle task still occupied capacity during Blocking", err)
			}
		})
	}
}
