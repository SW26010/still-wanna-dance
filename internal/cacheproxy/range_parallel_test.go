package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

func TestParallelRangeDownloadClaimsEachBlockOnce(t *testing.T) {
	body := bytes.Repeat([]byte("range"), int(rangeBlockSize*9/5)+37)
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	var mu sync.Mutex
	counts := make(map[int64]int)
	active, peak := 0, 0
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		mu.Lock()
		counts[start]++
		active++
		peak = max(peak, active)
		if active == rangeBackgroundWorkers {
			unblock()
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Write(body[start : end+1])
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Prefetch(ctx, videoURL(string(body))); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	got, err := os.ReadFile(s.cfg.videoFile(v.key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("published bytes differ", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != rangeBackgroundWorkers {
		t.Fatalf("background concurrency = %d", peak)
	}
	if int64(len(counts)) != (int64(len(body))+rangeBlockSize-1)/rangeBlockSize {
		t.Fatal("missing blocks", counts)
	}
	for block, count := range counts {
		if count != 1 {
			t.Fatalf("block %d fetched %d times", block, count)
		}
	}
}

func TestParallelRangeGlobalBudgetLeavesRoomForPlaybackAndSeek(t *testing.T) {
	bodies := [][]byte{
		bytes.Repeat([]byte("a"), int(rangeBlockSize*9)),
		bytes.Repeat([]byte("b"), int(rangeBlockSize*9)),
		bytes.Repeat([]byte("c"), int(rangeBlockSize*9)),
	}
	bodyByKey := make(map[string][]byte)
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	type event struct {
		key   string
		start int64
	}
	started := make(chan event, 64)
	var mu sync.Mutex
	active, peak := 0, 0
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("e")
		body := bodyByKey[key]
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		started <- event{key, start}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Write(body[start : end+1])
	})
	var videos []video
	for _, body := range bodies {
		r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
		v, _ := s.parseResolved(r)
		videos = append(videos, v)
		bodyByKey[v.key] = body
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var flights []*flight
	for _, v := range videos[:2] {
		f, _, err := s.obtainMode(ctx, v, true)
		if err != nil {
			t.Fatal(err)
		}
		flights = append(flights, f)
	}
	waitRequestQueue(t, s.requests, 2) // six workers, four background permits
	for range backgroundRequestLimit {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("background failed to fill quota")
		}
	}
	f, playback, err := s.obtain(ctx, videos[2])
	if err != nil {
		t.Fatal("cold playback could not start", err)
	}
	defer playback.Close()
	flights = append(flights, f)
	seek := flights[0].spool.reader(ctx, videos[0].size)
	defer seek.Close()
	seek.Seek(8*rangeBlockSize, io.SeekStart)
	seekDone := make(chan error, 1)
	go func() {
		got := make([]byte, 17)
		_, err := io.ReadFull(seek, got)
		if err == nil && !bytes.Equal(got, bodies[0][:17]) {
			err = fmt.Errorf("seek bytes differ")
		}
		seekDone <- err
	}()
	seenPlayback, seenSeek := false, false
	for !seenPlayback || !seenSeek {
		select {
		case e := <-started:
			seenPlayback = seenPlayback || e.key == videos[2].key && e.start == 0
			seenSeek = seenSeek || e.key == videos[0].key && e.start == 8*rangeBlockSize
		case <-ctx.Done():
			t.Fatal("playback/Seek waited behind blocked background work")
		}
	}
	mu.Lock()
	observedPeak := peak
	mu.Unlock()
	if observedPeak > videoRequestLimit {
		t.Fatalf("peak = %d, exceeds %d", observedPeak, videoRequestLimit)
	}
	unblock()
	if err := <-seekDone; err != nil {
		t.Fatal(err)
	}
	for _, flight := range flights {
		select {
		case <-flight.done:
		case <-ctx.Done():
			t.Fatal("download did not complete")
		}
		if flight.err != nil {
			t.Fatal(flight.err)
		}
	}
	mu.Lock()
	observedPeak = peak
	mu.Unlock()
	if observedPeak > videoRequestLimit {
		t.Fatalf("global peak = %d", observedPeak)
	}
}

func TestRangeSchedulingPrioritizesReadersThenNearbyBlocks(t *testing.T) {
	w := newRangeWork(20 * rangeBlockSize)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	advance := w.trackReader(ctx)
	advance(12 * rangeBlockSize)
	releaseTail := w.demand(17 * rangeBlockSize)
	defer releaseTail()
	releaseHead := w.demand(2 * rangeBlockSize)
	defer releaseHead()
	for _, want := range []int64{17, 2, 13, 14, 1} {
		got, _, ok := w.next(ctx, true)
		if !ok || got != want {
			t.Fatalf("next = %d, want %d", got, want)
		}
		w.finish(got)
	}
	advance(6 * rangeBlockSize)
	got, _, _ := w.next(ctx, true)
	if got != 7 {
		t.Fatalf("read-ahead did not follow seek: %d", got)
	}
	w.finish(got)
	cancel()
	// A canceled cursor is ineffective even before its async cleanup runs.
	got, _, _ = w.next(context.Background(), true)
	if got != 3 {
		t.Fatalf("canceled reader retained pre-read priority: %d", got)
	}
}
