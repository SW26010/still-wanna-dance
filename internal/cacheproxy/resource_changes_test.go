package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func futureGET(t *testing.T, s *Server, key string, blocks int64) (*downloadSession, *downloadDemand) {
	t.Helper()
	v := video{key: key, size: blocks * rangeBlockSize}
	clock := &playbackClock{at: time.Now().Add(time.Hour), limit: v.size, media: estimateMediaTiming(v.size, 0)}
	ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
	ctx = context.WithValue(ctx, httpGETKey{}, true)
	d, release := addTestDemand(t, s, ctx, v, true)
	t.Cleanup(release)
	return testResourceSession(t, s, v), d
}

func TestConsumptionOnlyNotifiesItsResource(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint("shared=", shared), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
				a, d := futureGET(t, s, "a", 2)
				b, _ := futureGET(t, s, "b", 4)
				if shared {
					futureGET(t, s, "a", 2)
				}
				_, changedA, _ := a.plan(time.Now(), true)
				before, changedB, _ := b.plan(time.Now(), true)
				capacity := s.DemandCapacityChanged()
				g := &getConsumption{s: s, demand: d, size: a.v.size}
				g.start(http.StatusOK, nil)
				time.Sleep(time.Millisecond)
				g.accept(rangeBlockSize)
				assertNotification(t, changedA, true)
				assertNotification(t, changedB, false)
				assertNotification(t, capacity, !shared)
				after, _, _ := b.plan(time.Now(), true)
				for block, use := range before {
					if after[block] != use {
						t.Errorf("unrelated resource block %d changed after consumption", block)
					}
				}
			})
		})
	}
}

func assertNotification(t *testing.T, changed <-chan struct{}, want bool) {
	t.Helper()
	notified := false
	select {
	case <-changed:
		notified = true
	default:
	}
	if notified != want {
		t.Errorf("notification=%v, want %v", notified, want)
	}
}

func demandPublicationLog(count *atomic.Int32) *slog.Logger {
	return slog.New(flightEventHandler{
		Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
		handle: func(r slog.Record) {
			if r.Message == "block_demand" {
				count.Add(1)
			}
		},
	})
}

func TestDownloadLeaseRenewalIgnoresOrdinaryReplans(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		w, _ := futureGET(t, s, "content", 4)
		start := time.Now()
		plan, _, _ := w.plan(start, true)
		var publications atomic.Int32
		w.f.log = demandPublicationLog(&publications)
		for block, use := range plan {
			task, demand, err := s.scheduler.Schedule(fmt.Sprint("future-", block), rangeBlockSize,
				func(context.Context, *blocksched.Progress) blocksched.Result { panic("future task executed") }, use.need)
			if err != nil {
				t.Fatal(err)
			}
			w.jobs[block] = &scheduledBlock{task: task, merged: demand, published: use.need}
		}
		for second := 1; second <= 35; second++ {
			time.Sleep(time.Second)
			plan, changed, active := w.plan(time.Now(), true)
			publications.Store(0)
			if _, _, err := w.reconcile(plan, time.Now(), changed, active); err != nil {
				t.Fatal(err)
			}
			want := 0
			if second%10 == 0 {
				want = len(plan)
			}
			if got := int(publications.Load()); got != want {
				t.Errorf("at %ds published %d blocks, want %d", second, got, want)
			}
			for _, j := range w.jobs {
				if j.published.LeaseUntil != start.Add(time.Duration(30+second/10*10)*time.Second) {
					t.Fatalf("lease drifted between renewals at %ds: %v", second, j.published.LeaseUntil)
				}
			}
		}
	})
}

func TestResourceSnapshotRejectsOnlyRelevantChanges(t *testing.T) {
	s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
	a := testResourceSession(t, s, video{key: "a", size: rangeBlockSize})
	b := testResourceSession(t, s, video{key: "b", size: rangeBlockSize})
	_, changedA, _ := a.plan(time.Now(), true)
	_, changedB, _ := b.plan(time.Now(), true)
	futureGET(t, s, "b", 1)
	if !s.preemptFlight(a.f, changedA) || s.preemptFlight(b.f, changedB) {
		t.Fatal("retirement ignored a new source or depended on an unrelated resource")
	}
	_, changedA, _ = a.plan(time.Now(), true)
	_, changedB, _ = b.plan(time.Now(), true)
	capacity := s.DemandCapacityChanged()
	s.notifyResourceAvailability("b")
	assertNotification(t, changedA, false)
	assertNotification(t, changedB, true)
	assertNotification(t, capacity, false)
}

func TestPreparationObservesSharingAndReleasesSubscription(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelWaiting), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
				s.ctx = context.Background()
				_, release := addTestDemand(t, s, context.Background(), video{key: "full", size: backgroundUseBlocks * rangeBlockSize}, false)
				defer release()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				v := video{key: "pending", size: rangeBlockSize}
				go func() {
					_, closeSource, err := s.addPreparation(ctx, v)
					if closeSource != nil {
						closeSource()
					}
					result <- err
				}()
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatal("preparation did not wait", err)
				default:
				}
				capacity := s.DemandCapacityChanged()
				if cancelWaiting {
					cancel()
				} else {
					// A GET can use reserved headroom. Sharing its block must
					// admit the pending preparation without any capacity release.
					_, closeGET := addTestDemand(t, s, context.Background(), v, true)
					defer closeGET()
				}
				err := <-result
				if cancelWaiting && !errors.Is(err, context.Canceled) || !cancelWaiting && err != nil {
					t.Fatal("wrong preparation outcome", err)
				}
				assertNotification(t, capacity, false)
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.resourceChanges) != 0 {
					t.Fatal("completed/canceled preparation retained an unused subscription")
				}
			})
		})
	}
}

func TestGETClockChangeNotifiesDependentQueueResources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		if err := s.UpdatePrefetchQueue([]int64{1, 2}, false); err != nil {
			t.Fatal(err)
		}
		registerQueueResource(t, s, "1", "queue-a")
		registerQueueResource(t, s, "2", "queue-b")
		var sessions []*downloadSession
		var changes []<-chan struct{}
		for _, key := range []string{"queue-a", "queue-b", "unrelated"} {
			w := testResourceSession(t, s, video{key: key, size: 20 * rangeBlockSize})
			_, changed, _ := w.plan(time.Now(), true)
			sessions = append(sessions, w)
			changes = append(changes, changed)
		}
		end := time.Now().Add(time.Hour)
		release := holdQueuePlaybackUntil(t, s, end)
		for i, changed := range changes {
			assertNotification(t, changed, i < 2)
		}
		plan, _, _ := sessions[0].plan(time.Now(), true)
		if plan[0].need.ReadyBy != end || protectionFor(s, "queue-a", 0).start != end.Add(-s.protectionAhead()) {
			t.Fatal("queue download and protection failed to follow GET clock")
		}
		_, changed, _ := sessions[1].plan(time.Now(), true)
		release()
		// Leaving live playback changes idle eligibility even when the
		// future queue start remains exactly the same.
		assertNotification(t, changed, true)
	})
}

func TestQueueResourceReplacementNotifiesBothIdentities(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		if err := s.UpdatePrefetchQueue([]int64{1}, false); err != nil {
			t.Fatal(err)
		}
		registerQueueResource(t, s, "1", "old")
		old := testResourceSession(t, s, video{key: "old", size: 20 * rangeBlockSize})
		next := testResourceSession(t, s, video{key: "new", size: 20 * rangeBlockSize})
		_, oldChanged, _ := old.plan(time.Now(), true)
		_, newChanged, _ := next.plan(time.Now(), true)
		registerQueueResource(t, s, "1", "new")
		assertNotification(t, oldChanged, true)
		assertNotification(t, newChanged, true)
		plan, _, _ := old.plan(time.Now(), true)
		if len(plan) != 0 || !protectionFor(s, "old", 0).start.IsZero() {
			t.Fatal("replaced queue content retained active usage/protection")
		}
		plan, _, _ = next.plan(time.Now(), true)
		if len(plan) != 20 || protectionFor(s, "new", 0).start.IsZero() {
			t.Fatal("replacement content lost usage/protection")
		}
	})
}

func TestScheduledRenewalTimerSurvivesFrequentResourceEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
		w, _ := futureGET(t, s, "content", 2)
		var publications atomic.Int32
		log := demandPublicationLog(&publications)
		f := &flight{discard: true, log: log, streaming: make(chan struct{}),
			progress: &downloadProgress{log: log},
			ranges:   &localResource{video: w.v, done: make(map[int64]bool)}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- s.publishScheduled(ctx, "", w.v, f) }()
		synctest.Wait()
		for second := 1; second <= 35; second++ {
			time.Sleep(time.Second)
			if second < 9 || second > 11 {
				s.notifyResourceAvailability(w.v.key)
			}
			synctest.Wait()
			// Includes a quiet interval around the first renewal, then a
			// continuous stream of events through the next two renewals.
			if got, want := int(publications.Load()), 2*(1+second/10); got != want {
				t.Fatalf("at %ds publications=%d, want %d", second, got, want)
			}
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal("coordinator failed during lease renewal", err)
		}
	})
}
