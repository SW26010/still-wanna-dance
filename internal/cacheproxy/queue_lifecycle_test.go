package cacheproxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func assertUsageWithdrawn(t *testing.T, j *scheduledBlock) {
	t.Helper()
	if j.merged != nil || !j.nextExpiry.IsZero() {
		t.Fatal("expired source retained its ordinary contribution", j.nextExpiry)
	}
	synctest.Wait()
	select {
	case <-j.task.Done():
		if !errors.Is(j.task.Err(), context.Canceled) {
			t.Fatal("last contribution ended with an unexpected result", j.task.Err())
		}
	default:
		t.Fatal("task survived without ordinary usage or local reads")
	}
}

func demandLifecycleSession(t *testing.T) (*downloadSession, *Server) {
	t.Helper()
	w, s := testSession(t)
	task, registered, err := s.scheduler.Schedule("queue", rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: context.Canceled}
	}, blocksched.Need{Owner: "fixture", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	w.jobs[0] = &scheduledBlock{task: task, merged: registered}
	return w, s
}

func registerQueueResource(t *testing.T, s *Server, song, key string) {
	t.Helper()
	_, release := addTestDemand(t, s, s.queueDemandContext(context.Background(), song), video{key: key, songID: song, size: 20 * rangeBlockSize}, false)
	t.Cleanup(release)
}

func reconcileSession(t *testing.T, w *downloadSession) {
	t.Helper()
	for {
		now := time.Now()
		plan, changed, active := w.plan(now, false)
		_, retry, err := w.reconcile(plan, now, changed, active)
		if err != nil {
			t.Fatal(err)
		}
		if !retry {
			return
		}
	}
}

func TestQueueReorderUsesOneTimeBase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := demandLifecycleSession(t)
		base := time.Now().Add(2 * time.Minute)
		holdQueuePlaybackUntil(t, s, base)
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
		s.mu.Lock()
		raw, _ := testSourcePlan(s, w.v.key, time.Now())
		s.mu.Unlock()
		for _, p := range raw[0] {
			if p.source == sourceQueue && p.need.ReadyBy != base.Add(2*typicalDuration) {
				t.Fatal("unchanged third position retained previous epoch", p.need.ReadyBy)
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
		{"promoted_then_removed", []int64{1, 2}, []int64{2, 1}, time.Second, true},
		{"demoted_then_removed", []int64{2, 1}, []int64{1, 2}, time.Second, false},
		{"expires_at_observation", []int64{1, 2}, []int64{2, 1}, 2 * time.Second, false},
		{"expired_before_observation", []int64{1, 2}, []int64{2, 1}, 3 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := demandLifecycleSession(t)
				s.UpdatePrefetchQueue(tc.before, false)
				registerQueueResource(t, s, "2", w.v.key)
				reconcileSession(t, w)
				time.Sleep(time.Second)
				s.UpdatePrefetchQueue(tc.middle, false)
				source := s.prefetchSources["2"]
				time.Sleep(time.Second)
				s.UpdatePrefetchQueue([]int64{1}, false)
				removedAt := time.Now()
				time.Sleep(tc.delay) // No coordinator observation of the intermediate state.
				reconcileSession(t, w)
				j := w.jobs[0]
				if tc.handoff {
					if j.retiring || !j.nextExpiry.Equal(removedAt.Add(2*time.Second)) {
						t.Fatalf("missing/freshened handoff: retiring=%v expiry=%v", j.retiring, j.nextExpiry)
					}
					// Preserve the removed head's actual deadline, even if past.
					if !j.published.ReadyBy.Equal(source.readyBy(0).Add(2 * time.Second)) {
						t.Fatal("handoff used the stale ordinary-queue deadline", j.published.ReadyBy)
					}
					reconcileSession(t, w)
					if !j.nextExpiry.Equal(removedAt.Add(2 * time.Second)) {
						t.Fatal("withdrawal was replayed or renewed")
					}
				} else {
					assertUsageWithdrawn(t, j)
				}
			})
		})
	}
}
