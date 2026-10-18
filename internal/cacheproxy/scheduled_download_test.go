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
	v := video{key: "content", size: 20 * rangeBlockSize}
	f := &flight{ranges: &rangeDownload{done: make(map[int64]bool)}}
	w := &downloadSession{s: s, ctx: context.Background(), v: v, f: f, wake: make(chan struct{}, 1), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
	w.initialized = make(chan struct{})
	close(w.initialized)
	return w, s
}

func TestBlockDemandFollowsActualWaitAndIndependentReaders(t *testing.T) {
	w, s := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, release := s.addDemand(ctx, w.v, true)
	defer release()
	plan, _, _ := w.plan(time.Now(), true)
	for _, needs := range plan {
		for _, n := range needs {
			if n.need.Blocking {
				t.Fatal("GET created synthetic Blocking")
			}
		}
	}
	one, two := w.demand(12*rangeBlockSize), w.demand(12*rangeBlockSize)
	count := func() int {
		plan, _, _ := w.plan(time.Now(), true)
		n := 0
		for _, p := range plan[12] {
			if p.need.Blocking {
				n++
			}
		}
		return n
	}
	if count() != 2 {
		t.Fatal("independent demands merged destructively")
	}
	one()
	one()
	if count() != 1 {
		t.Fatal("one reader removed another")
	}
	two()
	if count() != 0 {
		t.Fatal("completed wait stayed blocking")
	}
	cancel()
	plan, _, _ = w.plan(time.Now(), true)
	if len(plan) != 0 {
		t.Fatal("canceled context renewed itself")
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
		_, release := s.addDemand(s.queueDemandContext(context.Background(), "2"), video{key: w.v.key, songID: "2"}, false)
		defer release()
		now := time.Now()
		plan, _, _ := w.plan(now, false)
		key := blockSource{"queue", s.prefetchSources["2"]}
		before, ok := plan[0][key]
		if !ok || before.source != sourceQueue || before.need.ReadyBy.IsZero() {
			t.Fatal("ordinary queue source missing")
		}
		s.UpdatePrefetchQueue([]int64{2, 1}, false)
		plan, _, _ = w.plan(now, false)
		after, ok := plan[0][key]
		if !ok || after.source != sourceQueueHead || !after.need.ReadyBy.Equal(before.need.ReadyBy.Add(-typicalDuration)) {
			t.Fatal("queue reorder did not update deadline")
		}
		job, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
			<-ctx.Done()
			return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
		})
		w.jobs[0] = &scheduledBlock{task: job, needs: make(map[blockSource]plannedNeed)}
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
		if handoff := w.jobs[0].handoffUntil; !handoff.Equal(now.Add(queueHandoff)) {
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
	w.jobs[0] = &scheduledBlock{task: job, needs: make(map[blockSource]plannedNeed)}
	now := time.Now()
	read := blockSource{"get", "a"}
	queue := blockSource{"queue", "b"}
	need := blocksched.Need{Owner: "shared", ReadyBy: now, LeaseUntil: now.Add(sourceLease)}
	plan := map[int64]map[blockSource]plannedNeed{0: {read: {need: need, source: sourceGET}, queue: {need: need, source: sourceQueue}}}
	w.reconcile(plan, now, nil, false)
	delete(plan[0], read)
	w.reconcile(plan, now, nil, false)
	if len(w.jobs[0].needs) != 1 || !w.jobs[0].handoffUntil.Equal(now.Add(getHandoff)) {
		t.Fatal("independent source lost or bad GET handoff")
	}
	job.Close()
	<-job.Done()
}

func TestHandoffDefersFutureDeadlineWithoutExtendingValidity(t *testing.T) {
	now := time.Now()
	for _, ready := range []time.Time{now.Add(time.Minute), now.Add(-time.Minute), {}} {
		original := blocksched.Need{Owner: "video", Blocking: true, ReadyBy: ready, LeaseUntil: now.Add(sourceLease)}
		h := handoffNeed(original, now, getHandoff, 2*time.Second)
		if h.Blocking || !h.LeaseUntil.Equal(now.Add(getHandoff)) {
			t.Fatal("handoff kept blocking or original long validity", h)
		}
		if ready.After(now) && !h.ReadyBy.Equal(ready.Add(2*time.Second)) {
			t.Fatal("future work was promoted by disconnect", h)
		}
		if !ready.After(now) && !h.ReadyBy.Equal(now.Add(2*time.Second)) {
			t.Fatal("overdue work retained stale urgency", h)
		}
		if !original.Blocking {
			t.Fatal("independent source was mutated")
		}
	}
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
				_, release := s.addDemand(context.Background(), w.v, false)
				defer release()
				plan, _, _ := w.plan(time.Now(), initialized)
				if _, ok := plan[1]; !ok {
					t.Fatal("remaining ranges were not planned")
				}
			}
		})
	}
}
