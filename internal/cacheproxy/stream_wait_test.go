package cacheproxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestLocalReadWithdrawsOnlyItsOwnWait(t *testing.T) {
	for _, exit := range []string{"client", "server", "close", "success", "failure"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := blocksched.New(blocksched.Config{SoftLimit: 1})
				defer s.Close()
				reads := s.NewReadRequests("video", func(int64) string { return "block" }, nil)
				defer reads.Close()
				registered := make(chan struct{}, 2)
				sp := &spool{blocks: newBlockFiles(t.TempDir(), 1, nil), changed: make(chan struct{}), refs: 1,
					demand: func(offset int64) func() {
						end := reads.Wait(offset / rangeBlockSize)
						registered <- struct{}{}
						return end
					}}
				defer sp.release()
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				one, two := sp.reader(ctx, 1), sp.reader(context.Background(), 1)
				defer one.Close()
				defer two.Close()
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { _, err := one.Read(make([]byte, 1)); first <- err }()
				go func() { _, err := two.Read(make([]byte, 1)); second <- err }()
				<-registered
				<-registered
				failure := errors.New("read failed")
				var want error
				switch exit {
				case "client":
					cancel(nil)
					want = context.Canceled
				case "server":
					cancel(failure)
					want = failure
				case "close":
					one.Close()
					want = context.Canceled
				case "success":
					if _, err := sp.WriteAt([]byte{1}, 0); err != nil {
						t.Fatal(err)
					}
				case "failure":
					sp.finish(failure)
					want = failure
				}
				if err := <-first; !errors.Is(err, want) {
					t.Fatal("first read lost its result", err, want)
				}
				synctest.Wait()
				if exit != "success" && exit != "failure" {
					if !reads.Requested()[0] {
						t.Fatal("one canceled reader removed the other wait")
					}
					two.Close()
				}
				<-second
				one.Close()
				two.Close()
				synctest.Wait()
				if len(reads.Requested()) != 0 {
					t.Fatal("finished/canceled reads leaked waits")
				}
			})
		})
	}
}

func TestReadCancellationDuringWaitRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := blocksched.New(blocksched.Config{SoftLimit: 1})
		defer s.Close()
		reads := s.NewReadRequests("video", func(int64) string { return "block" }, nil)
		defer reads.Close()
		enter, proceed := make(chan struct{}), make(chan struct{})
		sp := &spool{blocks: newBlockFiles(t.TempDir(), 1, nil), changed: make(chan struct{}), refs: 1,
			demand: func(int64) func() { close(enter); <-proceed; return reads.Wait(0) }}
		defer sp.release()
		ctx, cancel := context.WithCancel(context.Background())
		reader := sp.reader(ctx, 1)
		defer reader.Close()
		done := make(chan error, 1)
		go func() { _, err := reader.Read(make([]byte, 1)); done <- err }()
		<-enter
		cancel()
		close(proceed)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(reads.Requested()) != 0 {
			t.Fatal("late registration revived a canceled read")
		}
	})
}

func TestReadWaitDoesNotChangeUsageOrHandoffClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		now := time.Now()
		clock := &playbackClock{at: now.Add(time.Hour), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), playbackClockKey{}, clock))
		_, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		before, _, _ := w.plan(now, true)
		end := w.reads.Wait(0)
		during, _, _ := w.plan(now, true)
		if before[0] != during[0] {
			t.Fatal("read urgency entered the ordinary use contract")
		}
		cancel()
		synctest.Wait()
		handoff, _, _ := w.plan(now, true)
		if handoff[0].need.ReadyBy != clock.at.Add(handoffGrace) || handoff[0].need.LeaseUntil != now.Add(handoffGrace) {
			t.Fatal("handoff inherited actual read time instead of the source clock")
		}
		end()
		after, _, _ := w.plan(now, true)
		if after[0] != handoff[0] {
			t.Fatal("read cleanup withdrew or changed handoff usage")
		}
	})
}

func TestOrdinaryDemandCapacityDoesNotCancelReadExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1, MaxDemands: 1})
		now := time.Now()
		n := blocksched.Need{Owner: "video", ReadyBy: now.Add(time.Hour), LeaseUntil: now.Add(2 * time.Hour)}
		_, occupied, err := s.scheduler.Schedule("other", 1, nil, n)
		if err != nil {
			t.Fatal(err)
		}
		w := newDownloadSession(s, context.Background(), video{key: "video", size: rangeBlockSize}, &flight{})
		defer w.close(nil)
		endRead := w.reads.Wait(0)
		defer endRead()
		started := make(chan struct{})
		task, _, err := s.scheduler.Schedule(w.reads.Key(0), rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
			close(started)
			<-ctx.Done()
			return blocksched.Result{Err: ctx.Err()}
		}, blocksched.Need{})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		// A previously deferred task has no handle. Reconciliation must attach
		// the read-only execution even without an ordinary usage contribution.
		w.jobs[0] = &scheduledBlock{}
		if _, _, err := w.reconcile(map[int64]blockUse{0: {}}, now, nil, true); err != nil || w.jobs[0].task != task {
			t.Fatal("deferred read-only execution was not attached", err)
		}
		plan := map[int64]blockUse{0: {need: n, nextExpiry: n.LeaseUntil}}
		if _, _, err := w.reconcile(plan, now, nil, true); err != nil || w.capacity == nil {
			t.Fatal("ordinary capacity shortage was not deferred", err)
		}
		synctest.Wait()
		select {
		case <-task.Done():
			t.Fatal("failed ordinary registration canceled a local read", task.Err())
		default:
		}
		occupied.Close()
		if _, _, err := w.reconcile(plan, now, nil, true); err != nil || w.jobs[0].merged == nil || !w.jobs[0].merged.Accepted() {
			t.Fatal("ordinary usage did not attach when capacity returned", err)
		}
		endRead()
		select {
		case <-task.Done():
			t.Fatal("read cleanup canceled remaining ordinary usage", task.Err())
		default:
		}
	})
}
