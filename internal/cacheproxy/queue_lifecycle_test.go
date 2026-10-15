package cacheproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func demandLifecycleSession(t *testing.T) (*downloadSession, *Server) {
	t.Helper()
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
	t.Cleanup(s.scheduler.Close)
	task, err := s.scheduler.Submit("queue", rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: blocksched.ErrPaused, Remaining: rangeBlockSize}
	})
	if err != nil {
		t.Fatal(err)
	}
	w.jobs[0] = &scheduledBlock{task: task, needs: make(map[blockSource]plannedNeed)}
	return w, s
}

func registerQueueResource(t *testing.T, s *Server, song, key string) {
	t.Helper()
	_, release := s.addDemand(s.queueDemandContext(context.Background(), song), video{key: key, songID: song}, false)
	t.Cleanup(release)
}

func reconcileSession(t *testing.T, w *downloadSession) {
	t.Helper()
	now := time.Now()
	plan, _ := w.plan(now, false)
	if _, _, err := w.reconcile(plan, now, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQueueReorderUsesOneTimeBase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := demandLifecycleSession(t)
		base := time.Now()
		s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
		registerQueueResource(t, s, "2", w.v.key)
		registerQueueResource(t, s, "3", w.v.key)
		reconcileSession(t, w)
		for i := 0; i < 7; i++ {
			time.Sleep(10 * time.Second)
			reconcileSession(t, w) // Keep the live sources' leases renewed.
		}
		time.Sleep(5 * time.Second)
		s.UpdatePrefetchQueue([]int64{2, 1, 3}, false)
		reconcileSession(t, w)
		j := w.jobs[0]
		if want := base; !j.published.ReadyBy.Equal(want) {
			t.Fatalf("new head lost priority: earliest=%v want=%v", j.published.ReadyBy, want)
		}
		for _, p := range j.needs {
			if p.source == sourceQueue && !p.need.ReadyBy.Equal(base.Add(2*typicalDuration)) {
				t.Fatal("unchanged third position retained the previous queue epoch", p.need.ReadyBy)
			}
		}
		ready := j.published.ReadyBy
		time.Sleep(5 * time.Second)
		s.UpdatePrefetchQueue([]int64{2, 1, 3}, false)
		reconcileSession(t, w)
		if !j.published.ReadyBy.Equal(ready) {
			t.Fatal("identical queue snapshot slid the deadline")
		}
	})
}

func TestCoalescedQueueChangesPreserveWithdrawalSource(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, middle []int64
		delay          time.Duration
		handoff        bool
	}{
		{"promoted_then_removed", []int64{1, 2}, []int64{2, 1}, 5 * time.Second, true},
		{"demoted_then_removed", []int64{2, 1}, []int64{1, 2}, 5 * time.Second, false},
		{"expired_before_observation", []int64{1, 2}, []int64{2, 1}, 21 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := demandLifecycleSession(t)
				s.UpdatePrefetchQueue(tc.before, false)
				registerQueueResource(t, s, "2", w.v.key)
				reconcileSession(t, w)
				time.Sleep(time.Second)
				s.UpdatePrefetchQueue(tc.middle, false)
				time.Sleep(time.Second)
				s.UpdatePrefetchQueue([]int64{1}, false)
				removedAt := time.Now()
				time.Sleep(tc.delay) // No coordinator observation of the intermediate state.
				reconcileSession(t, w)
				j := w.jobs[0]
				if tc.handoff {
					if j.retiring || len(j.handoffs) != 1 || !j.handoffUntil.Equal(removedAt.Add(queueHandoff)) {
						t.Fatalf("missing/freshened handoff: retiring=%v handoffs=%d until=%v", j.retiring, len(j.handoffs), j.handoffUntil)
					}
					// The head is already due at removal; handoff adds 5s from
					// withdrawal, independent of coordinator observation delay.
					if !j.handoffs[0].need.ReadyBy.Equal(removedAt.Add(5 * time.Second)) {
						t.Fatal("handoff used the stale ordinary-queue deadline", j.handoffs[0].need.ReadyBy)
					}
					reconcileSession(t, w)
					if len(j.handoffs) != 1 || !j.handoffUntil.Equal(removedAt.Add(queueHandoff)) {
						t.Fatal("withdrawal was replayed or renewed")
					}
				} else if !j.retiring || len(j.handoffs) != 0 {
					t.Fatal("ordinary/expired source was retained", j.handoffUntil)
				}
			})
		})
	}
}
