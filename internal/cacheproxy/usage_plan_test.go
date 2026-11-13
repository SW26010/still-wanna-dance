package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestUnifiedHandoffKeepsRunningWorkWithoutStartingRemainder(t *testing.T) {
	for _, kind := range []string{"client", "head"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				w.v.size = 2 * rangeBlockSize
				clock := &playbackClock{at: time.Now().Add(-5 * time.Second), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
				ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
				if kind == "head" {
					s.UpdatePrefetchQueue([]int64{1}, false)
					w.v.songID = "1"
					ctx = s.queueDemandContext(ctx, "1")
				}
				_, release := addTestDemand(t, s, ctx, w.v, kind == "client")
				defer release()
				plan, _, _ := w.plan(time.Now())
				started := make(chan int64, 2)
				complete := make(chan struct{})
				for block := int64(0); block < 2; block++ {
					n := plan[block].need
					task, demand, err := s.scheduler.Schedule(w.reads.Key(block), rangeBlockSize, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
						started <- block
						if block == 0 {
							select {
							case <-complete:
								return blocksched.Result{}
							case <-ctx.Done():
							}
						} else {
							<-ctx.Done()
						}
						return blocksched.Result{Err: ctx.Err()}
					}, n)
					if err != nil {
						t.Fatal(err)
					}
					w.jobs[block] = &scheduledBlock{task: task, merged: demand, published: n}
				}
				if block := <-started; block != 0 {
					t.Fatal("unexpected first execution", block)
				}
				time.Sleep(time.Second)
				exit := time.Now()
				release()
				if kind == "head" {
					// An idle queue's rolling clock can legitimately move while
					// the source still exists. Freeze its last source clock.
					plan, _, _ = w.plan(time.Now())
					s.UpdatePrefetchQueue(nil, false)
				}
				reconcileSession(t, w)
				for block, job := range w.jobs {
					if !job.published.WindowClosed || job.published.ReadyBy != plan[block].need.ReadyBy || job.published.LeaseUntil != exit.Add(handoffGrace) {
						t.Fatal("handoff changed original clock or supplied ordinary start permission", block, job.published)
					}
				}
				task := w.jobs[0].task
				close(complete)
				if err := task.Err(); err != nil {
					t.Fatal("source exit canceled running work", err)
				}
				w.f.ranges.done[0] = true
				if err := w.reapCompleted(); err != nil {
					t.Fatal(err)
				}
				reconcileSession(t, w)
				synctest.Wait()
				if len(started) != 0 {
					t.Fatal("handoff started queued work after the slot was released")
				}
				// An independent GET can use the same task immediately; ending it
				// leaves the original handoff in charge of the running lifecycle.
				independent := context.WithValue(context.Background(), playbackClockKey{}, clock)
				_, end := addTestDemand(t, s, context.WithValue(independent, httpGETKey{}, true), w.v, true)
				reconcileSession(t, w)
				if block := <-started; block != 1 {
					t.Fatal("independent source failed to open the remaining task", block)
				}
				end()
				reconcileSession(t, w)
				time.Sleep(time.Until(exit.Add(handoffGrace)))
				reconcileSession(t, w)
				if err := w.jobs[1].task.Err(); !errors.Is(err, context.Canceled) {
					t.Fatal("handoff expiry did not end unsupported work", err)
				}
			})
		})
	}
}

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
				for source, p := range sources {
					if source.kind != "handoff" || !p.need.WindowClosed || p.need.Opportunistic || p.need.ReadyBy != clock.ready(block*rangeBlockSize) || p.need.LeaseUntil != now.Add(handoffGrace) {
						t.Fatal("handoff lost original clock, fixed expiry or source semantics", p)
					}
				}
			}
			plan, _, _ := w.plan(now)
			if len(plan) != 17 || plan[1].need.Owner != "" || plan[2].need.Owner != "" {
				t.Fatal("download adapter scheduled already readable handoff blocks", len(plan))
			}
			synctest.Wait()
			s.mu.Lock()
			if len(s.useHandoffs) != 1 || len(s.demands) != 0 {
				t.Error("cancellation callback replayed source transfer")
			}
			s.mu.Unlock()
			time.Sleep(handoffGrace)
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
