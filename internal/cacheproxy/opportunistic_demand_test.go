package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestAggregatePreservesIndependentUntimedDemand(t *testing.T) {
	now := time.Now()
	get, batch := blockSource{"get", 1}, blockSource{"batch", 2}
	ready := now.Add(time.Hour)
	j := &scheduledBlock{needs: map[blockSource]plannedNeed{
		get:   {source: sourceGET, need: blocksched.Need{Owner: "v", ReadyBy: ready, LeaseUntil: now.Add(time.Hour)}},
		batch: {source: sourceBatch, need: blocksched.Need{Owner: "v", LeaseUntil: now.Add(time.Second)}},
	}}
	n := j.aggregate(now)
	if !n.Opportunistic || n.Blocking || n.ReadyBy != ready || j.nextExpiry != now.Add(time.Second) {
		t.Fatal("mixed summary", n, j.nextExpiry)
	}
	if n = j.aggregate(now.Add(time.Second)); n.Opportunistic || n.ReadyBy != ready {
		t.Fatal("expired batch retained early admission", n)
	}
	delete(j.needs, batch)
	if n = j.aggregate(now); n.Opportunistic || n.ReadyBy != ready {
		t.Fatal("withdrawn batch retained early admission", n)
	}
	// Handoffs retain only their computed finite deadline, never early admission.
	h := handoffNeed(blocksched.Need{Opportunistic: true, Blocking: true}, now, getHandoff, time.Second)
	if h.Opportunistic || h.Blocking || h.ReadyBy.IsZero() {
		t.Fatal("handoff inherited execution privileges", h)
	}
}

func TestBatchCompletesFutureTailSharedWithGET(t *testing.T) {
	s, _ := setup(t, nil)
	body := bytes.Repeat([]byte("b"), int(rangeBlockSize+123))
	var calls atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			return &http.Response{StatusCode: 206, ContentLength: end - start + 1, Request: r,
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}}, Body: io.NopCloser(bytes.NewReader(body[start : end+1]))}, nil
		})}
	}
	url := videoURL(string(body))
	r, _ := http.NewRequest("GET", url, nil)
	v, _ := s.parseResolved(r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	playbackCtx := context.WithValue(ctx, playbackClockKey{}, &playbackClock{at: time.Now(), media: mediaTiming{v.size, 15 * time.Minute}})
	f, reader, err := s.obtain(playbackCtx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := io.ReadFull(reader, make([]byte, rangeBlockSize)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("tail entered future window early")
	}
	// A separate batch wants completion now, without turning the GET into a
	// Blocking read or erasing its future playback deadline.
	if _, err := s.Prefetch(ctx, url); err != nil {
		t.Fatal("batch could not use idle capacity for shared tail", err)
	}
	receiveDemandEvent(t, f.done)
	if f.err != nil || calls.Load() != 2 {
		t.Fatal("shared task failed or duplicated", f.err, calls.Load())
	}
	got, err := os.ReadFile(s.cfg.videoFile(v.key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("batch did not publish full content", err)
	}
	got, err = io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, body[rangeBlockSize:]) {
		t.Fatal("batch completion invalidated GET reader", err)
	}
}
