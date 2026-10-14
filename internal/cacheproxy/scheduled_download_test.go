package cacheproxy

import (
	"context"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// Storage/transport fault fixtures need known simultaneous operations. Ramp and
// adaptation are tested separately; this sets only the execution policy.
func fixedBlockScheduler(s *Server) {
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{Limit: 6, Background: 4, PreemptAfter: 20 * time.Millisecond, Cooldown: 20 * time.Millisecond})
}

func testSession(t *testing.T) (*downloadSession, *Server) {
	t.Helper()
	s := &Server{capacityChanged: make(chan struct{})}
	v := video{key: "content", size: 20 * rangeBlockSize}
	f := &flight{ranges: &rangeDownload{done: make(map[int64]bool)}}
	w := &downloadSession{s: s, ctx: context.Background(), v: v, f: f, started: time.Now(), wake: make(chan struct{}, 1), cursors: make(map[*scheduledCursor]bool), waits: make(map[*scheduledWait]bool), jobs: make(map[int64]*scheduledBlock)}
	return w, s
}

func TestBlockDemandFollowsActualWaitAndIndependentReaders(t *testing.T) {
	w, s := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, release := s.addDemand(ctx, w.v, true)
	defer release()
	advance := w.track(ctx)
	advance(12 * rangeBlockSize)
	plan, _ := w.plan(time.Now(), true)
	for _, needs := range plan {
		for _, n := range needs {
			if n.need.Blocking {
				t.Fatal("GET/window created synthetic Blocking")
			}
		}
	}
	if _, ok := plan[14]; !ok {
		t.Fatal("missing finite read ahead")
	}
	if _, ok := plan[15]; ok {
		t.Fatal("unbounded read ahead")
	}
	one, two := w.demand(12*rangeBlockSize), w.demand(12*rangeBlockSize)
	count := func() int {
		plan, _ := w.plan(time.Now(), true)
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
	advance(4 * rangeBlockSize)
	plan, _ = w.plan(time.Now(), true)
	if _, ok := plan[14]; ok {
		t.Fatal("Seek retained old read window")
	}
	cancel()
	plan, _ = w.plan(time.Now(), true)
	if len(plan) != 0 {
		t.Fatal("canceled context renewed itself")
	}
}

func TestBlockQueueOrderAndFiniteHandoff(t *testing.T) {
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{Limit: 1, Background: 1})
	defer s.scheduler.Close()
	// A blocked synthetic executor allows inspecting leases without network.
	w.s.scheduler = s.scheduler
	s.UpdatePrefetchQueue([]int64{1, 2}, false)
	_, release := s.addDemand(context.WithValue(context.Background(), queueDemandKey{}, true), video{key: w.v.key, songID: "2"}, false)
	defer release()
	now := time.Now()
	plan, _ := w.plan(now, false)
	old := plan[0][blockSource{"queue", "2"}].need.ReadyBy
	s.UpdatePrefetchQueue([]int64{2, 1}, false)
	plan, _ = w.plan(now, false)
	if !plan[0][blockSource{"queue", "2"}].need.ReadyBy.Before(old) {
		t.Fatal("queue reorder did not update deadline")
	}
	job, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
	})
	w.jobs[0] = &scheduledBlock{task: job, needs: make(map[blockSource]attachedNeed)}
	if _, err := w.reconcile(plan, now); err != nil {
		t.Fatal(err)
	}
	s.UpdatePrefetchQueue(nil, true)
	plan, _ = w.plan(now, false)
	until, err := w.reconcile(plan, now)
	if err != nil || !until.Equal(now.Add(queueHandoff)) {
		t.Fatal(until, err)
	}
	// An unrelated later event must not slide the abandoned queue's lease.
	later, _ := w.reconcile(plan, now.Add(time.Second))
	if !later.Equal(until) {
		t.Fatal("handoff was extended without a source")
	}
	job.Close()
	<-job.Done()
}

func TestBlockLeaseRemovalDoesNotDropOtherOwner(t *testing.T) {
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{Limit: 1, Background: 1})
	defer s.scheduler.Close()
	job, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
	})
	w.jobs[0] = &scheduledBlock{task: job, needs: make(map[blockSource]attachedNeed)}
	now := time.Now()
	read := blockSource{"read", "a"}
	queue := blockSource{"queue", "b"}
	need := blocksched.Need{Owner: "shared", ReadyBy: now, LeaseUntil: now.Add(sourceLease)}
	plan := map[int64]map[blockSource]plannedNeed{0: {read: {need: need}, queue: {need: need}}}
	w.reconcile(plan, now)
	delete(plan[0], read)
	w.reconcile(plan, now)
	if len(w.jobs[0].needs) != 1 || !w.jobs[0].handoffUntil.Equal(now.Add(getHandoff)) {
		t.Fatal("independent source lost or bad GET handoff")
	}
	job.Close()
	<-job.Done()
}
