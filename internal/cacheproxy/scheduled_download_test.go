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
	s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
	return testResourceSession(t, s, video{key: "content", size: 20 * rangeBlockSize}), s
}

func testSessionServer(t *testing.T, cfg blocksched.Config) *Server {
	t.Helper()
	s := &Server{cfg: Config{AggressiveCacheReclamation: "auto", LocalProtectionGrace: DefaultLocalProtectionGrace}, capacityChanged: make(chan struct{}), scheduler: blocksched.New(cfg)}
	t.Cleanup(s.scheduler.Close)
	t.Cleanup(func() {
		s.mu.Lock()
		s.closed = true
		if s.useTimer != nil {
			s.useTimer.Stop()
		}
		s.mu.Unlock()
	})
	return s
}

func testResourceSession(t *testing.T, s *Server, v video) *downloadSession {
	t.Helper()
	f := &flight{ranges: &localResource{done: make(map[int64]bool)}}
	w := newDownloadSession(s, context.Background(), v, f)
	t.Cleanup(w.reads.Close)
	t.Cleanup(w.releaseResourceWatch)
	close(w.initialized)
	return w
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
	if len(w.reads.Requested()) != 0 {
		t.Fatal("GET created a synthetic read request")
	}
	one, two := w.reads.Wait(12), w.reads.Wait(12)
	if !w.reads.Requested()[12] {
		t.Fatal("independent demands merged destructively")
	}
	one()
	one()
	if !w.reads.Requested()[12] {
		t.Fatal("one reader removed another")
	}
	two()
	if w.reads.Requested()[12] {
		t.Fatal("completed wait stayed blocking")
	}
	cancel()
	plan, _, _ = w.plan(time.Now(), true)
	for _, p := range plan {
		if p.need.LeaseUntil.After(time.Now().Add(handoffGrace)) {
			t.Fatal("canceled context renewed itself", p)
		}
	}
}

func TestBlockQueueOrderAndFiniteHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		// A blocked synthetic executor allows inspecting leases without network.
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
		job, registered, _ := s.scheduler.Schedule("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
			<-ctx.Done()
			return blocksched.Result{Err: context.Canceled}
		}, blocksched.Need{Owner: "fixture", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)})
		w.jobs[0] = &scheduledBlock{task: job, merged: registered}
		if _, _, err := w.reconcile(plan, now, nil, false); err != nil {
			t.Fatal(err)
		}
		s.UpdatePrefetchQueue(nil, true)
		plan, _, _ = w.plan(now, false)
		until, _, err := w.reconcile(plan, now, nil, false)
		if err != nil || !until.Equal(now.Add(handoffGrace)) {
			t.Fatal(until, err)
		}
		w.jobs[0].task.Watch(w.wake)
		// Handoff must defer the original queue deadline, not make it more urgent.
		// (The plan was obtained at the same now for both queue orders.)
		if ready := w.jobs[0].published.ReadyBy; !ready.Equal(after.need.ReadyBy.Add(2 * time.Second)) {
			t.Fatal("handoff changed the original queue clock", ready)
		}
		if handoff := w.jobs[0].published.LeaseUntil; !handoff.Equal(now.Add(handoffGrace)) {
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
	job, registered, _ := s.scheduler.Schedule("test", 1, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: context.Canceled}
	}, blocksched.Need{Owner: "fixture", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)})
	w.jobs[0] = &scheduledBlock{task: job, merged: registered}
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
			// Coordinator snapshots before the worker has received response headers.
			initialized := false
			task, _, err := s.scheduler.Schedule("first", rangeBlockSize, func(context.Context, *blocksched.Progress) blocksched.Result {
				if ranged {
					w.f.ranges.done[0] = true // Only one of twenty blocks exists.
					close(w.initialized)
				}
				return blocksched.Result{}
			}, blocksched.Need{Owner: "video", LeaseUntil: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			w.jobs[0] = &scheduledBlock{task: task}

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
