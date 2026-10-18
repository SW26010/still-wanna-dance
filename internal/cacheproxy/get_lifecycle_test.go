package cacheproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestGETWithdrawalTimeSurvivesDelayedReconciliation(t *testing.T) {
	for _, exit := range []string{"cancel", "close"} {
		for _, delay := range []time.Duration{time.Second, 3 * time.Second} {
			t.Run(exit+"/"+delay.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w, s := demandLifecycleSession(t)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					_, release := s.addDemand(ctx, w.v, true)
					defer release()
					reconcileSession(t, w)
					ready := w.jobs[0].published.ReadyBy
					time.Sleep(time.Second)
					removedAt := time.Now()
					if exit == "cancel" {
						cancel()
					} else {
						release()
					}
					synctest.Wait()
					// The source exits now, but the coordinator handles it later.
					time.Sleep(delay)
					cancel()
					release() // Repeated cleanup must not reset the withdrawal time.
					synctest.Wait()
					reconcileSession(t, w)
					j := w.jobs[0]
					if delay >= getHandoff {
						if !j.retiring || len(j.handoffs) != 0 {
							t.Fatalf("expired GET acquired fresh grace: retiring=%v handoffs=%d until=%v", j.retiring, len(j.handoffs), j.handoffUntil)
						}
						return
					}
					until := removedAt.Add(getHandoff)
					if j.retiring || len(j.handoffs) != 1 || !j.handoffUntil.Equal(until) {
						t.Fatalf("GET handoff slid with processing: retiring=%v handoffs=%d until=%v want=%v", j.retiring, len(j.handoffs), j.handoffUntil, until)
					}
					h := j.handoffs[0].need
					if h.Blocking || !h.ReadyBy.Equal(ready.Add(2*time.Second)) {
						t.Fatal("handoff used processing time or retained Blocking", h)
					}
					reconcileSession(t, w)
					if len(j.handoffs) != 1 || j.handoffs[0].need != h {
						t.Fatal("withdrawal replayed or renewed its handoff")
					}
					time.Sleep(time.Until(until))
					reconcileSession(t, w)
					if !j.retiring || len(j.handoffs) != 0 {
						t.Fatal("GET survived its original expiry")
					}
				})
			})
		}
	}
}
