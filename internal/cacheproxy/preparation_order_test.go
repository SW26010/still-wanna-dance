package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestPreparationOrderSurvivesRetriesAndAppendsAfterIdle(t *testing.T) {
	_, s := testSession(t)
	ctx := context.Background()
	first, _ := WithPreparationAdmission(ctx)
	check := func(ctx context.Context, key string, want uint64) {
		t.Helper()
		d, release, err := s.addPreparation(ctx, video{key: key, size: 2 * s.cfg.BlockSize})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		s.mu.Lock()
		plan, _ := s.usePlanLocked(key, time.Now())
		s.mu.Unlock()
		if d.preparationOrder != want || len(plan) != 2 {
			t.Fatal("incorrect preparation identity", d.preparationOrder, want, len(plan))
		}
		for _, use := range plan {
			if use.need.OpportunisticOrder != want || !use.need.Opportunistic {
				t.Fatal("block projection lost preparation order", use.need)
			}
		}
	}
	check(first, "first", 1)
	check(ctx, "direct", 2) // No admission notifier is required.
	check(first, "fallback-address", 1)
	check(ctx, "later", 3) // Empty demand registry does not reset the counter.

	// Neither actual playback nor queue preparation acquires manual order.
	get, release := addTestDemand(t, s, first, video{key: "get", size: 1}, true)
	if get.preparationOrder != 0 {
		t.Fatal("GET inherited manual order")
	}
	release()
	queueCtx := context.WithValue(first, queueDemandKey{}, (*queueSource)(nil))
	d, release, err := s.addPreparation(queueCtx, video{key: "queue", size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d.preparationOrder != 0 {
		t.Fatal("queue inherited manual order")
	}
	release()
	check(ctx, "after-playback", 4)
}

func TestPreparationOrderAssignedOnlyOnAdmissionAndNeverWraps(t *testing.T) {
	_, s := testSession(t)
	ctx := withPreparationScope(context.Background())
	_, release := addTestDemand(t, s, context.Background(), video{key: "full", size: backgroundUseBlocks * s.cfg.BlockSize}, false)
	if _, _, err := s.addDemand(ctx, video{key: "pending", size: 1}, false); !errors.Is(err, ErrDemandCapacity) {
		t.Fatal("expected capacity refusal", err)
	}
	if s.preparationSeq != 0 {
		t.Fatal("failed admission consumed an order")
	}
	release()
	s.preparationSeq = ^uint64(0) - 1
	d, release, err := s.addPreparation(ctx, video{key: "last", size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d.preparationOrder != ^uint64(0) {
		t.Fatal("last order not available")
	}
	release()
	if _, _, err := s.addPreparation(context.Background(), video{key: "overflow", size: 1}); err == nil {
		t.Fatal("order wrapped into the default class")
	}
	if len(s.useBlocks) != 0 || len(s.demands) != 0 {
		t.Fatal("order exhaustion leaked usage")
	}
	// Existing operations remain retryable even at the counter boundary.
	_, release, err = s.addPreparation(ctx, video{key: "retry", size: 1})
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAggregatePreparationOrderUsesOnlyLiveEarlySources(t *testing.T) {
	now := time.Now()
	timed := plannedNeed{source: sourceGET, need: blocksched.Need{Owner: "v", ReadyBy: now.Add(time.Hour), LeaseUntil: now.Add(time.Hour)}}
	manual := func(order uint64) plannedNeed {
		return plannedNeed{source: sourceBatch, need: blocksched.Need{Owner: "v", LeaseUntil: now.Add(time.Hour), OpportunisticOrder: order}}
	}
	first, second := manual(1), manual(2)
	first.need.LeaseUntil = now.Add(time.Second)
	plan := map[blockSource]plannedNeed{{"get", 0}: timed, {"batch", 1}: first, {"batch", 2}: second}
	check := func(at time.Time, want uint64) {
		t.Helper()
		n := aggregateUse(plan, at).need
		if n.OpportunisticOrder != want || !n.Opportunistic || n.ReadyBy != timed.need.ReadyBy {
			t.Fatal("incorrect mixed order", n, want)
		}
	}
	check(now, 1)
	check(now.Add(time.Second), 2)
	first.need.WindowClosed = true
	plan[blockSource{"batch", 1}] = first
	check(now, 2)
	plan[blockSource{"unordered", 0}] = manual(0)
	check(now, 0)
	delete(plan, blockSource{"unordered", 0})
	delete(plan, blockSource{"batch", 1})
	check(now, 2)
	delete(plan, blockSource{"batch", 2})
	if n := aggregateUse(plan, now).need; n.Opportunistic || n.OpportunisticOrder != 0 {
		t.Fatal("withdrawal retained manual order", n)
	}
}

// Real HTTP Range responses and the normal hash/publication path. Observe
// scheduler admissions directly: concurrent HTTP arrivals can be reordered.
func TestManualPreparationConcentratesRangesAndPublishesFirstResource(t *testing.T) {
	const blockSize, blocks = 4096, 8
	bodies := []string{strings.Repeat("a", blockSize*blocks), strings.Repeat("b", blockSize*blocks), strings.Repeat("c", blockSize*blocks)}
	byKey := make(map[string]string)
	for _, body := range bodies {
		byKey[videoKey(body)] = body
	}
	type pending struct {
		key     string
		release chan struct{}
	}
	requests := make(chan pending, 32)
	started := make(chan string, blocks*len(bodies))
	finish := make(chan struct{})
	laterRequest := make(chan struct{})
	noteLaterRequest := sync.OnceFunc(func() { close(laterRequest) })
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("e")
		body, ok := byKey[key]
		var first, last int64
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// An admitted last block may reach HTTP after a later resource uses
		// the other free slot. Force that valid arrival order every time.
		if key == videoKey(bodies[0]) && first == blockSize*(blocks-1) {
			select {
			case <-laterRequest:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(last-first+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		p := pending{key, make(chan struct{})}
		requests <- p
		if key != videoKey(bodies[0]) {
			noteLaterRequest()
		}
		select {
		case <-p.release:
		case <-finish:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(body[first : last+1]))
	}, func(cfg *Config) {
		cfg.BlockSize, cfg.DownloadSoftLimit, cfg.DownloadGrowInterval = blockSize, 2, 0
		cfg.ObserveBlockSchedule = func(e blocksched.Event) {
			if e.Kind == "started" {
				started <- e.Key
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		index int
		err   error
	}
	done := make(chan result, len(bodies))
	go func() { _, err := s.Prefetch(ctx, videoURL(bodies[0])); done <- result{0, err} }()
	next := func() pending {
		t.Helper()
		select {
		case p := <-requests:
			return p
		case r := <-done:
			t.Fatalf("unexpected early result: %+v", r)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return pending{}
	}
	first := next()
	wake := make(chan struct{}, 1)
	stop := s.scheduler.WatchChanges(wake)
	defer stop()
	waitTasks := func(count int) {
		t.Helper()
		for len(s.scheduler.Observation().Tasks) < count {
			select {
			case <-wake:
			case <-ctx.Done():
				t.Fatal("preparations did not all register", ctx.Err())
			}
		}
	}
	// Before initialization only one block is schedulable. Wait for the
	// complete first-resource plan so later sources cannot fill that gap.
	waitTasks(blocks)
	for i := 1; i < len(bodies); i++ {
		operation, admitted := WithPreparationAdmission(ctx)
		go func() { _, err := s.Prefetch(operation, videoURL(bodies[i])); done <- result{i, err} }()
		select {
		case <-admitted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	waitTasks(blocks + len(bodies) - 1)
	for i := range blocks {
		select {
		case key := <-started:
			if want := downloadTaskKey(videoKey(bodies[0]), int64(len(bodies[0])), int64(i)); key != want {
				t.Fatalf("scattered admissions after %d/%d first-resource blocks: got %s, want %s", i, blocks, key, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		p := first
		if i > 0 {
			p = next()
		}
		for p.key != videoKey(bodies[0]) {
			// Later HTTP arrivals may overtake an already admitted first-resource
			// block. Hold their bodies until the first publication is verified.
			p = next()
		}
		close(p.release)
	}
	select {
	case r := <-done:
		if r.index != 0 || r.err != nil {
			t.Fatal("first resource not published", r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	got, err := os.ReadFile(cfg.videoFile(videoKey(bodies[0])))
	if err != nil || !bytes.Equal(got, []byte(bodies[0])) {
		t.Fatal("first publication has incorrect content", err)
	}
	t.Logf("first resource published after %d contiguous Range admissions; later sources were already admitted", blocks)
	close(finish)
	for range len(bodies) - 1 {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
