package cacheproxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestSourceFilteringKeepsIndependentLeases(t *testing.T) {
	now := time.Now()
	get, queue := blockSource{"get", 1}, blockSource{"queue", 2}
	need := func(ready, lease time.Duration) blocksched.Need {
		return blocksched.Need{Owner: "shared", ReadyBy: now.Add(ready), LeaseUntil: now.Add(lease)}
	}
	j := &scheduledBlock{
		needs: map[blockSource]plannedNeed{
			get:   {need: need(5*time.Second, 10*time.Second), source: sourceGET},
			queue: {need: need(7*time.Second, 30*time.Second), source: sourceQueue},
		},
		handoffs: []handoffContribution{{need: need(2*time.Second, 20*time.Second)}},
	}
	n := j.aggregate(now)
	if !n.ReadyBy.Equal(now.Add(5*time.Second)) || n.Blocking {
		t.Fatal("old handoff urgency survived a nonblocking GET", n)
	}
	if !n.LeaseUntil.Equal(now.Add(30*time.Second)) || !j.nextExpiry.Equal(now.Add(10*time.Second)) {
		t.Fatal("winner overwrote another lease", n)
	}
	if len(j.handoffs) != 1 || !j.handoffs[0].need.LeaseUntil.Equal(now.Add(20*time.Second)) {
		t.Fatal("suppression deleted or extended handoff")
	}
	// The queue can still compete on its deadline: GET only covers handoffs.
	p := j.needs[queue]
	p.need.ReadyBy = now.Add(time.Second)
	j.needs[queue] = p
	if n := j.aggregate(now); !n.ReadyBy.Equal(now.Add(time.Second)) {
		t.Fatal("GET suppressed independent queue", n)
	}
	p.need.ReadyBy = now.Add(7 * time.Second)
	j.needs[queue] = p
	// Expiry works even before the source record has been physically removed.
	if n := j.aggregate(now.Add(11 * time.Second)); !n.ReadyBy.Equal(now.Add(2 * time.Second)) {
		t.Fatal("expired GET kept covering handoff", n)
	}
	if n := j.aggregate(now.Add(21 * time.Second)); !n.ReadyBy.Equal(now.Add(7*time.Second)) || len(j.handoffs) != 0 {
		t.Fatal("handoff renewed itself or removed queue", n)
	}
}

func TestQueuePromotionDemotionDoesNotCreateHandoff(t *testing.T) {
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
	defer s.scheduler.Close()
	task, _ := s.scheduler.Submit("test", 1, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
		<-ctx.Done()
		return blocksched.Result{Err: blocksched.ErrPaused, Remaining: 1}
	})
	w.jobs[0] = &scheduledBlock{task: task, needs: make(map[blockSource]plannedNeed)}
	now := time.Now()
	key := blockSource{"queue", "song"}
	for i, origin := range []demandSource{sourceQueue, sourceQueueHead, sourceQueue} {
		p := plannedNeed{source: origin, revision: int64(i), need: blocksched.Need{Owner: "shared", ReadyBy: now.Add(time.Duration(i+1) * time.Second), LeaseUntil: now.Add(sourceLease)}}
		if _, _, err := w.reconcile(map[int64]map[blockSource]plannedNeed{0: {key: p}}, now, nil, false); err != nil {
			t.Fatal(err)
		}
		j := w.jobs[0]
		if len(j.handoffs) != 0 || j.needs[key].source != origin || !j.published.ReadyBy.Equal(p.need.ReadyBy) {
			t.Fatal("reorder created handoff or kept stale deadline")
		}
	}
	// Its current source is ordinary queue; removal must end it directly.
	if until, _, err := w.reconcile(nil, now, nil, false); err != nil || !until.IsZero() {
		t.Fatal("ordinary queue removal acquired grace", until, err)
	}
	if err := task.Err(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestQueueSourceIdentityOnlySurvivesContinuousMembership(t *testing.T) {
	_, s := testSession(t)
	s.UpdatePrefetchQueue([]int64{1, 2}, false)
	first := s.prefetchSources["1"]
	oldContext := s.queueDemandContext(context.Background(), "1")
	s.UpdatePrefetchQueue([]int64{2, 1}, false)
	if s.prefetchSources["1"] != first {
		t.Fatal("reorder replaced original source")
	}
	s.UpdatePrefetchQueue([]int64{2}, false)
	s.UpdatePrefetchQueue([]int64{1, 2}, false)
	next := s.prefetchSources["1"]
	if next == first {
		t.Fatal("removed queue source was revived")
	}
	s.UpdatePrefetchQueue([]int64{1, 2}, true)
	if s.prefetchSources["1"] == next {
		t.Fatal("room reset inherited old source attributes")
	}
	late, release := addTestDemand(t, s, oldContext, video{songID: "1", key: "old-result"}, false)
	defer release()
	if s.validQueueSourceLocked(late) || s.prefetchSources["1"].use.key != "" {
		t.Fatal("late URL result revived a canceled queue source")
	}
	fresh, releaseFresh := addTestDemand(t, s, s.queueDemandContext(context.Background(), "1"), video{songID: "1", key: "new-result"}, false)
	defer releaseFresh()
	if !s.validQueueSourceLocked(fresh) || s.prefetchSources["1"].use.key != "new-result" {
		t.Fatal("fresh queue source was not accepted")
	}
}

func TestBlockSourceMetadataIsBounded(t *testing.T) {
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
	defer s.scheduler.Close()
	task, _ := s.scheduler.Submit("test", 1, nil)
	w.jobs[0] = &scheduledBlock{task: task, needs: make(map[blockSource]plannedNeed)}
	now := time.Now()
	plan := map[int64]map[blockSource]plannedNeed{0: {}}
	for i := 0; i <= maxBlockContributions; i++ {
		plan[0][blockSource{"get", i}] = plannedNeed{source: sourceGET, need: blocksched.Need{Owner: "v", LeaseUntil: now.Add(sourceLease)}}
	}
	if _, _, err := w.reconcile(plan, now, nil, false); !errors.Is(err, blocksched.ErrCapacity) || !errors.Is(err, errBusy) {
		t.Fatal("source bound ignored", err)
	}
	task.Close()
}
