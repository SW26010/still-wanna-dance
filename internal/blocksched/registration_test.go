package blocksched

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestScheduleCapacityDefersFutureBlocksForUrgentWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 2, MaxDemands: 2})
		defer s.Close()
		future := Need{Owner: "future", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)}
		var pending []*Task
		for i := range 2 {
			task, _, err := s.Schedule(fmt.Sprint(i), 1, nil, future)
			if err != nil {
				t.Fatal(err)
			}
			pending = append(pending, task)
		}
		if _, _, err := s.Schedule("later", 1, nil, future); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		changed := s.CapacityChanged()
		started := make(chan struct{})
		task, _, err := s.Schedule("get", 1, func(ctx context.Context, _ *Progress) Result {
			close(started)
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, Need{Owner: "get", Blocking: true, LeaseUntil: future.LeaseUntil})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		<-changed
		deferred := 0
		for _, p := range pending {
			select {
			case <-p.Done():
				if !errors.Is(p.Err(), ErrDeferred) {
					t.Fatal(p.Err())
				}
				deferred++
			default:
			}
		}
		if deferred != 1 {
			t.Fatal("expected exactly one displaced future block", deferred)
		}
		changed = s.CapacityChanged()
		task.Close()
		<-task.Done()
		<-changed
		if _, _, err := s.Schedule("later", 1, nil, future); err != nil {
			t.Fatal("capacity did not recover", err)
		}
	})
}

func TestScheduleWaitsWhenAllMetadataIsExecuting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, MaxTasks: 1})
		defer s.Close()
		started := make(chan struct{})
		n := Need{Owner: "get", Blocking: true, LeaseUntil: time.Now().Add(time.Hour)}
		task, _, err := s.Schedule("active", 1, func(ctx context.Context, _ *Progress) Result {
			close(started)
			<-ctx.Done()
			return Result{Err: ctx.Err()}
		}, n)
		if err != nil {
			t.Fatal(err)
		}
		<-started
		if _, _, err := s.Schedule("waiting", 1, nil, n); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		task.Close()
		<-task.Done()
		if _, _, err := s.Schedule("waiting", 1, func(context.Context, *Progress) Result { return Result{} }, n); err != nil {
			t.Fatal(err)
		}
	})
}
