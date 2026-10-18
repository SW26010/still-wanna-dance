package cacheproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// Storage/transport fault fixtures need known simultaneous operations. Ramp and
// adaptation are tested separately; this sets only the execution policy.
func fixedBlockScheduler(s *Server) {
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 4})
}

func testSession(t *testing.T) (*downloadSession, *Server) {
	t.Helper()
	s := &Server{capacityChanged: make(chan struct{})}
	t.Cleanup(func() {
		s.mu.Lock()
		s.closed = true
		if s.useExpiry != nil {
			s.useExpiry.Stop()
		}
		s.mu.Unlock()
	})
	v := video{key: "content", size: 20 * rangeBlockSize}
	f := &flight{ranges: &rangeDownload{done: make(map[int64]bool)}}
	w := &downloadSession{s: s, ctx: context.Background(), v: v, f: f, wake: make(chan struct{}, 1), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
	w.initialized = make(chan struct{})
	close(w.initialized)
	return w, s
}

func addTestDemand(t *testing.T, s *Server, ctx context.Context, v video, playback bool) (*downloadDemand, func()) {
	t.Helper()
	d, release, err := s.addDemand(ctx, v, playback)
	if err != nil {
		t.Fatal(err)
	}
	return d, release
}

func TestBlockDemandFollowsActualWaitAndIndependentReaders(t *testing.T) {
	w, s := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, release := addTestDemand(t, s, ctx, w.v, true)
	defer release()
	plan, _, _ := w.plan(time.Now(), true)
	for _, p := range plan {
		if p.need.Blocking {
			t.Fatal("GET created synthetic Blocking")
		}
	}
	one, two := w.demand(12*rangeBlockSize), w.demand(12*rangeBlockSize)
	blocking := func() bool { plan, _, _ := w.plan(time.Now(), true); return plan[12].need.Blocking }
	if !blocking() {
		t.Fatal("independent demands merged destructively")
	}
	one()
	one()
	if !blocking() {
		t.Fatal("one reader removed another")
	}
	two()
	if blocking() {
		t.Fatal("completed wait stayed blocking")
	}
	cancel()
	plan, _, _ = w.plan(time.Now(), true)
	for _, p := range plan {
		if p.need.LeaseUntil.After(time.Now().Add(getHandoff)) {
			t.Fatal("canceled context renewed itself", p)
		}
	}
}

func TestBlockQueueOrderAndFiniteHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
		defer s.scheduler.Close()
		// A blocked synthetic executor allows inspecting leases without network.
		w.s.scheduler = s.scheduler
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		_, release := addTestDemand(t, s, s.queueDemandContext(context.Background(), "2"), video{key: w.v.key, songID: "2", size: w.v.size}, false)
		defer release()
		now := time.Now()
		plan, _, _ := w.plan(now, false)
		before, ok := plan[0]
		if !ok || before.need.ReadyBy.IsZero() {
			t.Fatal("ordinary queue source missing")
		}
		s.UpdatePrefetchQueue([]int64{2, 1}, false)
		plan, _, _ = w.plan(now, false)
		after, ok := plan[0]
		if !ok || !after.need.ReadyBy.Equal(before.need.ReadyBy.Add(-typicalDuration)) {
			t.Fatal("queue reorder did not update deadline")
		}
		job, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
			<-ctx.Done()
			return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
		})
		w.jobs[0] = &scheduledBlock{task: job}
		if _, _, err := w.reconcile(plan, now, nil, false); err != nil {
			t.Fatal(err)
		}
		s.UpdatePrefetchQueue(nil, true)
		plan, _, _ = w.plan(now, false)
		until, _, err := w.reconcile(plan, now, nil, false)
		if err != nil || !until.Equal(now.Add(queueHandoff)) {
			t.Fatal(until, err)
		}
		w.jobs[0].task.Watch(w.wake)
		// Handoff must defer the original queue deadline, not make it more urgent.
		// (The plan was obtained at the same now for both queue orders.)
		if handoff := w.jobs[0].published.LeaseUntil; !handoff.Equal(now.Add(queueHandoff)) {
			t.Fatal(handoff)
		}
		// An unrelated later event must not slide the abandoned queue's lease.
		later, _, _ := w.reconcile(plan, now.Add(time.Second), nil, false)
		if !later.Equal(until) {
			t.Fatal("handoff was extended without a source")
		}
		job.Close()
		<-job.Done()
	})
}

func TestBlockLeaseRemovalDoesNotDropOtherOwner(t *testing.T) {
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
	defer s.scheduler.Close()
	job, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
	})
	w.jobs[0] = &scheduledBlock{task: job}
	now := time.Now()
	read := blockSource{"get", "a"}
	queue := blockSource{"queue", "b"}
	need := blocksched.Need{Owner: "shared", ReadyBy: now, LeaseUntil: now.Add(sourceLease)}
	sources := map[blockSource]plannedNeed{read: {need: need, source: sourceGET}, queue: {need: need, source: sourceQueue}}
	plan := map[int64]blockUse{0: aggregateUse(sources, now)}
	w.reconcile(plan, now, nil, false)
	delete(sources, read)
	plan[0] = aggregateUse(sources, now)
	w.reconcile(plan, now, nil, false)
	if w.jobs[0].retiring || w.jobs[0].published != need {
		t.Fatal("independent source lost or task manufactured GET handoff")
	}
	job.Close()
	<-job.Done()
}

func TestCompletionRechecksRangeInitialization(t *testing.T) {
	for _, ranged := range []bool{false, true} {
		t.Run(map[bool]string{false: "sequential_published", true: "first_range_only"}[ranged], func(t *testing.T) {
			w, s := testSession(t)
			w.initialized = make(chan struct{})
			s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
			defer s.scheduler.Close()
			// Coordinator snapshots before the worker has received response headers.
			initialized := false
			task, err := s.scheduler.Submit("first", rangeBlockSize, func(context.Context, *blocksched.Progress) blocksched.Result {
				if ranged {
					w.f.ranges.done[0] = true // Only one of twenty blocks exists.
					close(w.initialized)
				}
				return blocksched.Result{}
			})
			if err != nil {
				t.Fatal(err)
			}
			w.jobs[0] = &scheduledBlock{task: task}
			task.Add(blocksched.Need{Owner: "video", LeaseUntil: time.Now().Add(time.Minute)})
			<-task.Done() // Worker initialization and completion precede observation.
			published, err := w.reapCompleted(&initialized)
			if err != nil || published == ranged || initialized != ranged || len(w.jobs) != 0 {
				t.Fatalf("published=%v initialized=%v jobs=%d err=%v", published, initialized, len(w.jobs), err)
			}
			if ranged {
				_, release := addTestDemand(t, s, context.Background(), w.v, false)
				defer release()
				plan, _, _ := w.plan(time.Now(), initialized)
				if _, ok := plan[1]; !ok {
					t.Fatal("remaining ranges were not planned")
				}
			}
		})
	}
}
