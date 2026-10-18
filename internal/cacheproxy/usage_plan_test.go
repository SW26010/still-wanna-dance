package cacheproxy

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestUsageHandoffIncludesReadableRemainderWithoutDownloadPlanning(t *testing.T) {
	for _, ahead := range []time.Duration{-time.Minute, time.Minute} {
		synctest.Test(t, func(t *testing.T) {
			w, s := testSession(t)
			now := time.Now()
			clock := &playbackClock{at: now.Add(ahead), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), playbackClockKey{}, clock))
			d, release := addTestDemand(t, s, ctx, w.v, true)
			defer release()
			w.f.ranges.done[1], w.f.ranges.done[2] = true, true
			g := &getConsumption{s: s, demand: d, size: w.v.size}
			g.start(http.StatusOK, nil)
			g.accept(rangeBlockSize)
			// Force planning to observe cancellation before AfterFunc takes mu.
			s.mu.Lock()
			cancel()
			usage, active := testSourcePlan(s, w.v.key, now)
			s.mu.Unlock()
			if !active || len(usage) != 19 || len(usage[0]) != 0 {
				t.Fatal("handoff failed to preserve only unconsumed usage", len(usage))
			}
			for block, sources := range usage {
				for _, p := range sources {
					if p.source != sourceHandoff || p.need.Blocking || p.need.Opportunistic || p.need.ReadyBy != clock.ready(block*rangeBlockSize).Add(2*time.Second) || p.need.LeaseUntil != now.Add(getHandoff) {
						t.Fatal("handoff lost original clock, fixed expiry or source semantics", p)
					}
				}
			}
			plan, _, _ := w.plan(now, true)
			if len(plan) != 17 || plan[1].need.Owner != "" || plan[2].need.Owner != "" {
				t.Fatal("download adapter scheduled already readable handoff blocks", len(plan))
			}
			synctest.Wait()
			s.mu.Lock()
			if len(s.useHandoffs) != 1 || len(s.demands) != 0 {
				t.Error("cancellation callback replayed source transfer")
			}
			s.mu.Unlock()
			time.Sleep(getHandoff)
			synctest.Wait()
			s.mu.Lock()
			usage, _ = testSourcePlan(s, w.v.key, time.Now())
			s.mu.Unlock()
			if len(usage) != 0 || useCount(s) != 0 {
				t.Fatal("handoff survived its original fixed expiry")
			}
		})
	}
}

func TestSourceExitOnlyCreatesAuthorizedHandoffs(t *testing.T) {
	for _, kind := range []string{"client", "server", "consumed", "manual", "shutdown", "head", "queue"} {
		t.Run(kind, func(t *testing.T) {
			w, s := testSession(t)
			ctx := context.Background()
			playback := kind != "manual" && kind != "head" && kind != "queue"
			if kind == "server" {
				ctx = context.WithValue(ctx, httpGETKey{}, true)
			}
			v := w.v
			if kind == "head" || kind == "queue" {
				s.UpdatePrefetchQueue([]int64{1, 2}, false)
				v.songID = "1"
				if kind == "queue" {
					v.songID = "2"
				}
				ctx = s.queueDemandContext(ctx, v.songID)
			}
			d, release := addTestDemand(t, s, ctx, v, playback)
			if kind == "consumed" {
				g := &getConsumption{s: s, demand: d, size: v.size}
				g.start(http.StatusOK, nil)
				g.accept(v.size)
			}
			if kind == "shutdown" {
				s.closed = true
			}
			release()
			if kind == "head" || kind == "queue" {
				s.UpdatePrefetchQueue(nil, false)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if got, want := len(s.useHandoffs) > 0, kind == "client" || kind == "head"; got != want {
				t.Fatal("wrong source exit conversion", got, want)
			}
		})
	}
}

// Source identity assertions stay inside collection tests. Runtime consumers
// receive only blockUse, before local availability or Blocking is applied.
func testSourcePlan(s *Server, key string, now time.Time) (map[int64]map[blockSource]plannedNeed, bool) {
	s.refreshUseSourcesLocked(now)
	projected, active := s.projectUseLocked(key, now)
	plan := make(map[int64]map[blockSource]plannedNeed, len(projected))
	for id, needs := range projected {
		plan[id.block] = needs
	}
	return plan, active
}
