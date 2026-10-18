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
	sources := map[blockSource]plannedNeed{
		get:                       {need: need(5*time.Second, 10*time.Second), source: sourceGET},
		queue:                     {need: need(7*time.Second, 30*time.Second), source: sourceQueue},
		blockSource{"handoff", 3}: {need: need(2*time.Second, 20*time.Second), source: sourceHandoff},
	}
	summary := aggregateUse(sources, now)
	n := summary.need
	if !n.ReadyBy.Equal(now.Add(5*time.Second)) || n.Blocking {
		t.Fatal("old handoff urgency survived a nonblocking GET", n)
	}
	if !n.LeaseUntil.Equal(now.Add(30*time.Second)) || !summary.nextExpiry.Equal(now.Add(10*time.Second)) {
		t.Fatal("winner overwrote another lease", n)
	}
	if summary.protectReady != now.Add(2*time.Second) {
		t.Fatal("download urgency filtering removed finite usage protection", summary)
	}
	if sources[blockSource{"handoff", 3}].need.LeaseUntil != now.Add(20*time.Second) {
		t.Fatal("suppression changed handoff expiry")
	}
	// The queue can still compete on its deadline: GET only covers handoffs.
	p := sources[queue]
	p.need.ReadyBy = now.Add(time.Second)
	sources[queue] = p
	if n := aggregateUse(sources, now).need; !n.ReadyBy.Equal(now.Add(time.Second)) {
		t.Fatal("GET suppressed independent queue", n)
	}
	p.need.ReadyBy = now.Add(7 * time.Second)
	sources[queue] = p
	// Expiry works even before the source record has been physically removed.
	if n := aggregateUse(sources, now.Add(11*time.Second)).need; !n.ReadyBy.Equal(now.Add(2 * time.Second)) {
		t.Fatal("expired GET kept covering handoff", n)
	}
	if n := aggregateUse(sources, now.Add(21*time.Second)).need; !n.ReadyBy.Equal(now.Add(7 * time.Second)) {
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
	w.jobs[0] = &scheduledBlock{task: task}
	now := time.Now()
	key := blockSource{"queue", "song"}
	for i, origin := range []demandSource{sourceQueue, sourceQueueHead, sourceQueue} {
		p := plannedNeed{source: origin, need: blocksched.Need{Owner: "shared", ReadyBy: now.Add(time.Duration(i+1) * time.Second), LeaseUntil: now.Add(sourceLease)}}
		if _, _, err := w.reconcile(map[int64]blockUse{0: aggregateUse(map[blockSource]plannedNeed{key: p}, now)}, now, nil, false); err != nil {
			t.Fatal(err)
		}
		j := w.jobs[0]
		if !j.published.ReadyBy.Equal(p.need.ReadyBy) {
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

func TestManySharedSourcesUseOneDownloadSummary(t *testing.T) {
	w, s := testSession(t)
	for range 150 {
		_, release := addTestDemand(t, s, context.Background(), w.v, false)
		defer release()
	}
	plan, _, _ := w.plan(time.Now(), true)
	if len(plan) != 20 || useCount(s) != 20 {
		t.Fatal("shared identities multiplied block quota", len(plan), useCount(s))
	}
	for _, p := range plan {
		if !p.need.Opportunistic || p.need.Owner != w.v.key {
			t.Fatal("invalid source-free summary", p)
		}
	}
}
