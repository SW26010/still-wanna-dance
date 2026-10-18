package blocksched

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestDownloadDeadlineStopsEvenWithBlockingAndUntimedOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 1})
		defer s.Close()
		started, cleanup := make(chan struct{}), make(chan struct{})
		stopped := make(chan error, 1)
		now := time.Now()
		n := Need{Owner: "get", ReadyBy: now, LeaseUntil: now.Add(time.Hour), Blocking: true}
		task, d, err := s.Schedule("shared", 100, func(ctx context.Context, _ *Progress) Result {
			close(started)
			<-ctx.Done()
			stopped <- context.Cause(ctx)
			<-cleanup
			return Result{Err: ctx.Err()}
		}, n)
		if err != nil {
			t.Fatal(err)
		}
		manual := task.Add(need("manual", false, 0, time.Hour))
		if !manual.Accepted() {
			t.Fatal("sharing failed")
		}
		<-started
		time.Sleep(20 * time.Second)
		n.LeaseUntil = time.Now().Add(time.Hour)
		n.Blocking = false
		if !d.Update(n) {
			t.Fatal("live lease update failed")
		}
		time.Sleep(10 * time.Second)
		if cause := <-stopped; !errors.Is(cause, ErrExpired) || time.Since(now) != DownloadGrace {
			t.Fatal("renewal, another owner or admission moved the deadline", cause, time.Since(now))
		}
		if d.Update(n) || manual.Update(need("manual", true, 0, time.Hour)) {
			t.Fatal("expired execution acquired new qualification")
		}
		select {
		case <-task.Done():
			t.Error("execution quota was released before response cleanup")
		default:
		}
		if _, _, err := s.Schedule("shared", 100, nil, need("manual", false, 0, time.Hour)); !errors.Is(err, ErrCapacity) {
			t.Error("replacement overlapped old cleanup", err)
		}
		close(cleanup)
		if !errors.Is(task.Err(), ErrExpired) {
			t.Fatal("lost deadline cause", task.Err())
		}
		replacement, _, err := s.Schedule("shared", 100, func(context.Context, *Progress) Result { return Result{} }, need("manual", false, 0, time.Hour))
		if err != nil || replacement == task || replacement.Err() != nil {
			t.Fatal("remaining untimed demand could not establish new work", err)
		}
	})
}

func TestQueuedDeadlineAndExpiredRegistrationNeverStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		holdStarted := make(chan struct{})
		hold, _, _ := s.Schedule("hold", 100, func(ctx context.Context, _ *Progress) Result {
			close(holdStarted)
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, need("manual", false, 0, time.Hour))
		<-holdStarted
		n := Need{Owner: "queue", ReadyBy: time.Now(), LeaseUntil: time.Now().Add(time.Hour)}
		queued, _, err := s.Schedule("late", 100, func(context.Context, *Progress) Result {
			t.Error("queued task started after its need-by window")
			return Result{}
		}, n)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(DownloadGrace)
		if !errors.Is(queued.Err(), ErrExpired) {
			t.Fatal("queue wait reset execution deadline", queued.Err())
		}
		n.Blocking, n.Opportunistic = true, true
		if _, _, err := s.Schedule("late", 100, nil, n); !errors.Is(err, ErrExpired) {
			t.Fatal("Blocking or untimed contribution bypassed an expired finite deadline", err)
		}
		select {
		case <-hold.Done():
			t.Fatal("finite deadline was applied to independent untimed work")
		default:
		}
	})
}

func TestActualNeedChangeRearmsDeadlineBeforeExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		now := time.Now()
		task, d, _ := s.Schedule("changed", 100, func(ctx context.Context, _ *Progress) Result {
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, Need{Owner: "owner", ReadyBy: now, LeaseUntil: now.Add(time.Hour)})
		time.Sleep(10 * time.Second)
		if !d.Update(Need{Owner: "replacement", ReadyBy: now.Add(time.Minute), LeaseUntil: now.Add(time.Hour)}) {
			t.Fatal("actual remaining demand change was rejected")
		}
		time.Sleep(20 * time.Second)
		select {
		case <-task.Done():
			t.Fatal("old deadline survived a real demand update before expiry")
		default:
		}
		time.Sleep(time.Minute)
		if !errors.Is(task.Err(), ErrExpired) || time.Since(now) != 90*time.Second {
			t.Fatal("updated finite deadline not enforced", task.Err(), time.Since(now))
		}
		if d.Update(need("late", false, time.Hour, 2*time.Hour)) {
			t.Fatal("late update revived expired task")
		}
	})
}
