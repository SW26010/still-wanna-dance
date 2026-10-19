package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestResourceDemandsExposeAllMissingBlocks(t *testing.T) {
	for _, kind := range []string{"get", "batch", "queue"} {
		t.Run(kind, func(t *testing.T) {
			w, s := testSession(t)
			w.v.size = 64 * rangeBlockSize
			now := time.Now()
			switch kind {
			case "queue":
				s.prefetchSources = map[string]*queueSource{"1": {
					song: "1", startAt: now, timing: mediaTiming{w.v.size, time.Minute},
					use: useSpan{w.v.key, 0, 64},
				}}
			default:
				_, release := addTestDemand(t, s, context.Background(), w.v, kind == "get")
				defer release()
			}
			plan, _, _ := w.plan(now, false)
			if len(plan) != 1 || plan[0].need.Owner == "" {
				t.Fatal("scheduled extra blocks before Range/entity initialization", len(plan))
			}
			w.f.ranges.done = map[int64]bool{0: true, 2: true, 63: true}
			plan, _, _ = w.plan(now, true)
			if len(plan) != 61 {
				t.Fatal("resource candidates were capped or completed blocks retained", len(plan))
			}
			for block := int64(0); block < 64; block++ {
				needs := plan[block]
				if w.f.ranges.done[block] {
					if needs.need.Owner != "" {
						t.Fatal("completed block retained", block)
					}
					continue
				}
				if needs.need.Owner == "" {
					t.Fatal("missing or duplicated source", block, needs)
				}
				if need := needs.need; need.ReadyBy.IsZero() != (kind == "batch") {
					t.Fatal("expanding candidates changed demand semantics", block, need)
				}
			}
		})
	}
}

func TestResourceDeadlinesRemainVisibleBeyondEightBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		other := testResourceSession(t, s, video{key: "later", size: rangeBlockSize})
		started, release := make(chan string, 1), make(chan struct{})
		run := func(key string) blocksched.Run {
			return func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
				started <- key
				select {
				case <-release:
				case <-ctx.Done():
				}
				return blocksched.Result{}
			}
		}
		now := time.Now()
		// Hold admission until both resources have submitted their real plans.
		_, _, err := s.scheduler.Schedule("gate", 1, run("gate"), blocksched.Need{Owner: "gate", LeaseUntil: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}

		<-started
		for i, session := range []*downloadSession{w, other} {
			clock := &playbackClock{at: now.Add(time.Duration(i) * 30 * time.Second), limit: session.v.size, media: mediaTiming{session.v.size, 20 * time.Second}}
			ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
			_, closeDemand := addTestDemand(t, s, ctx, session.v, true)
			defer closeDemand()
			plan, changed, active := session.plan(now, true)
			for block := int64(0); block < session.v.size/rangeBlockSize; block++ {
				key := fmt.Sprintf("%s/%d", session.v.key, block)
				task, merged, err := s.scheduler.Schedule(key, rangeBlockSize, run(key), plan[block].need)
				if err != nil {
					t.Fatal(err)
				}
				session.jobs[block] = &scheduledBlock{task: task, merged: merged, published: plan[block].need}
			}
			if _, _, err := session.reconcile(plan, now, changed, active); err != nil {
				t.Fatal(err)
			}
		}
		for block := range 20 {
			release <- struct{}{}
			if got, want := <-started, fmt.Sprintf("%s/%d", w.v.key, block); got != want {
				t.Fatalf("later resource bypassed an earlier block: got %s, want %s", got, want)
			}
		}
		release <- struct{}{}
		if got := <-started; got != "later/0" {
			t.Fatal("later resource did not resume", got)
		}
	})
}

func TestSingleVideoReachesGlobalSoftLimit(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 20*int(rangeBlockSize))
	started := make(chan int64, 32)
	gate := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(gate) })
	defer unblock()
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		started <- start / rangeBlockSize
		select {
		case <-gate:
			w.Write(body[start : end+1])
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// No Read/Seek: only one ordinary GET demand, using the production scheduler.
	seen := make(map[int64]bool)
	for len(seen) < videoRequestSoftLimit {
		select {
		case block := <-started:
			if seen[block] {
				t.Fatal("duplicate block request", block)
			}
			seen[block] = true
		case <-ctx.Done():
			t.Fatal("single video could not fill the global soft limit", len(seen))
		}
	}
	select {
	case block := <-started:
		t.Fatal("ordinary GET exceeded the global soft limit", block)
	case <-time.After(350 * time.Millisecond):
	}
	unblock()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("expanded download did not preserve content", len(got), err)
	}
	<-f.done
	if f.err != nil {
		t.Fatal("expanded download failed verification/publication", f.err)
	}
	for len(started) > 0 {
		block := <-started
		if seen[block] {
			t.Fatal("completed download repeated a block", block)
		}
		seen[block] = true
	}
	if len(seen) != 20 {
		t.Fatal("download omitted blocks", len(seen))
	}
}

func TestExpandedResourceRetainsGlobalMetadataCapacity(t *testing.T) {
	s := testSessionServer(t, blocksched.Config{SoftLimit: 16, MaxTasks: 16})
	w := testResourceSession(t, s, video{key: "content", size: 20 * rangeBlockSize})
	ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: time.Now().Add(time.Hour), limit: w.v.size, media: mediaTiming{w.v.size, time.Hour}})
	_, release := addTestDemand(t, s, ctx, w.v, true)
	defer release()
	plan, changed, active := w.plan(time.Now(), true)
	_, _, err := w.reconcile(plan, time.Now(), changed, active)
	if err != nil || len(w.jobs) != 16 || w.capacity == nil {
		t.Fatal("capacity should defer excess registration", len(w.jobs), err)
	}
	first := w.jobs[0].task
	first.Close()
	<-first.Done()
	initialized := true
	if _, err := w.reapCompleted(&initialized); err != nil || w.jobs[0] != nil {
		t.Fatal("stopped task was not retired before current usage is reconsidered", err)
	}
	w.f.ranges.done[0] = true
	plan, changed, active = w.plan(time.Now(), true)
	if _, _, err := w.reconcile(plan, time.Now(), changed, active); err != nil || w.jobs[16] == nil {
		t.Fatal("freed capacity was not reused", err)
	}
	w.close(err)
	for block := range 16 {
		if _, _, err := s.scheduler.Schedule(fmt.Sprint(block), 1, nil, blocksched.Need{Owner: "future", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)}); err != nil {
			t.Fatal("failed resource leaked task capacity", block, err)
		}
	}
}

func TestTwoLargeFutureVideosFillUsageCapacity(t *testing.T) {
	s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
	var sessions []*downloadSession
	for i := range 3 {
		session := testResourceSession(t, s, video{key: fmt.Sprintf("video-%d", i), size: 2 << 30})
		session.f.ranges.done[0] = true
		ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: time.Now().Add(time.Duration(i+1) * time.Hour), limit: session.v.size, media: mediaTiming{session.v.size, time.Hour}})
		_, release, err := s.addDemand(ctx, session.v, true)
		if i == 2 {
			if !errors.Is(err, ErrDemandCapacity) {
				t.Fatal("future usage exceeded its complete-span capacity", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		plan, changed, active := session.plan(time.Now(), true)
		if _, _, err := session.reconcile(plan, time.Now(), changed, active); err != nil {
			t.Fatal("new video failed at metadata capacity", err)
		}
		sessions = append(sessions, session)
	}
	if len(sessions[0].jobs) != 2047 || len(sessions[1].jobs) != 2047 || len(s.useBlocks) != maxUseBlocks {
		t.Fatal("completed blocks must retain usage without a download task", len(sessions[0].jobs), len(sessions[1].jobs), len(s.useBlocks))
	}
	started := make(chan struct{})
	foreground := testResourceSession(t, s, video{key: "foreground", size: 1})
	defer foreground.reads.Wait(0)()
	task, _, err := s.scheduler.Schedule(foreground.reads.Key(0), 1, func(context.Context, *blocksched.Progress) blocksched.Result {
		close(started)
		return blocksched.Result{}
	}, blocksched.Need{Owner: "foreground", LeaseUntil: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := task.Err(); err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		session.close(nil)
	}
}
