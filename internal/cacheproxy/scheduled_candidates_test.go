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
				s.prefetchResources = map[string]string{"1": w.v.key}
				s.prefetchSources = map[string]*queueSource{"1": {
					song: "1", startAt: now, timing: mediaTiming{w.v.size, time.Minute},
				}}
			default:
				_, release := s.addDemand(context.Background(), w.v, kind == "get")
				defer release()
			}
			plan, _ := w.plan(now, false)
			if len(plan) != 1 || len(plan[0]) != 1 {
				t.Fatal("scheduled extra blocks before Range/entity initialization", len(plan))
			}
			w.f.ranges.done = map[int64]bool{0: true, 2: true, 63: true}
			w.f.ranges.prefix = 1
			plan, _ = w.plan(now, true)
			if len(plan) != 61 {
				t.Fatal("resource candidates were capped or completed blocks retained", len(plan))
			}
			for block := int64(0); block < 64; block++ {
				needs := plan[block]
				if w.f.ranges.done[block] {
					if len(needs) != 0 {
						t.Fatal("completed block retained", block)
					}
					continue
				}
				if len(needs) != 1 {
					t.Fatal("missing or duplicated source", block, needs)
				}
				for source, need := range needs {
					if source.kind != kind || need.need.Blocking || need.need.ReadyBy.IsZero() != (kind == "batch") {
						t.Fatal("expanding candidates changed source semantics", block, source, need)
					}
				}
			}
		})
	}
}

func TestResourceDeadlinesRemainVisibleBeyondEightBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		other, _ := testSession(t)
		other.s, other.v.key, other.v.size = s, "later", rangeBlockSize
		s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
		defer s.scheduler.Close()
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
		gate, err := s.scheduler.Submit("gate", 1, run("gate"))
		if err != nil {
			t.Fatal(err)
		}
		gate.Add(blocksched.Need{Owner: "gate", LeaseUntil: now.Add(time.Hour)})
		<-started
		for i, session := range []*downloadSession{w, other} {
			clock := &playbackClock{at: now.Add(time.Duration(i) * 30 * time.Second), media: mediaTiming{session.v.size, 20 * time.Second}}
			ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
			_, closeDemand := s.addDemand(ctx, session.v, true)
			defer closeDemand()
			for block := int64(0); block < session.v.size/rangeBlockSize; block++ {
				key := fmt.Sprintf("%s/%d", session.v.key, block)
				task, err := s.scheduler.Submit(key, rangeBlockSize, run(key))
				if err != nil {
					t.Fatal(err)
				}
				session.jobs[block] = &scheduledBlock{task: task, needs: make(map[blockSource]plannedNeed)}
			}
			plan, changed := session.plan(now, true)
			if _, _, err := session.reconcile(plan, now, changed); err != nil {
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
	w, s := testSession(t)
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 16, MaxTasks: 16})
	defer s.scheduler.Close()
	_, release := s.addDemand(context.Background(), w.v, false)
	defer release()
	plan, changed := w.plan(time.Now(), true)
	_, _, err := w.reconcile(plan, time.Now(), changed)
	if !errors.Is(err, blocksched.ErrCapacity) || !errors.Is(err, errBusy) {
		t.Fatal("global metadata capacity was bypassed", err)
	}
	w.close(err)
	for block := range 16 {
		if _, err := s.scheduler.Submit(fmt.Sprint(block), 1, nil); err != nil {
			t.Fatal("failed resource leaked task capacity", block, err)
		}
	}
}
