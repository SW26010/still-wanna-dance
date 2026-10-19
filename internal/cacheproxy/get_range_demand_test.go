package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestGETPlanOnlyIncludesRequestedBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         []int64
	}{
		{"seek", fmt.Sprintf("bytes=%d-", 14*rangeBlockSize), []int64{14, 15, 16, 17, 18, 19}},
		{"single", fmt.Sprintf("bytes=%d-%d", 14*rangeBlockSize+17, 15*rangeBlockSize-1), []int64{14}},
		{"cross_boundary", fmt.Sprintf("bytes=%d-%d", 15*rangeBlockSize-1, 15*rangeBlockSize), []int64{14, 15}},
		{"suffix", "bytes=-23", []int64{19}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, s := testSession(t)
			r, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
			r.Header.Set("Range", tc.header)
			span, ok := playbackRange(r, w.v)
			if !ok {
				t.Fatal("invalid fixture range")
			}
			p := &playbackClock{at: time.Now(), offset: span.start, limit: span.end, media: mediaTiming{w.v.size, 200 * time.Second}}
			ctx := context.WithValue(context.Background(), playbackClockKey{}, p)
			_, release := addTestDemand(t, s, ctx, w.v, true)
			defer release()
			plan, _, active := w.plan(time.Now(), false)
			if !active || len(plan) != 1 || plan[tc.want[0]].need.Owner == "" {
				t.Fatal("initial request did not start at the requested block", plan)
			}
			plan, _, _ = w.plan(time.Now(), true)
			var got []int64
			for block, needs := range plan {
				got = append(got, block)
				if needs.need.Owner == "" {
					t.Fatal("GET created an extra source or synthetic Blocking", block, needs)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatal("GET crossed its range", got, tc.want)
			}
			// Background completion is independent, and must not inherit GET urgency.
			_, closeBatch := addTestDemand(t, s, context.Background(), w.v, false)
			defer closeBatch()
			plan, _, _ = w.plan(time.Now(), true)
			if len(plan) != 20 {
				t.Fatal("batch no longer covers the full resource", len(plan))
			}
			if n := plan[0].need; !n.ReadyBy.IsZero() || !n.Opportunistic {
				t.Fatal("prefix inherited playback urgency", n)
			}
			closeBatch()
			// Independent blocked readers still share just their current block.
			one, two := w.reads.Wait(tc.want[0]), w.reads.Wait(tc.want[0])
			if !w.reads.Requested()[tc.want[0]] {
				t.Fatal("lost independent waits")
			}
			one()
			if !w.reads.Requested()[tc.want[0]] {
				t.Fatal("one reader removed another")
			}
			two()
		})
	}
}

func TestCompletedGETSpanKeepsReaderWithoutDownloadingOtherBlocks(t *testing.T) {
	w, s := testSession(t)
	p := &playbackClock{at: time.Now(), offset: 14 * rangeBlockSize, limit: 15 * rangeBlockSize, media: mediaTiming{w.v.size, time.Minute}}
	_, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, p), w.v, true)
	defer release()
	w.f.ranges.done[14] = true
	now := time.Now()
	plan, changed, active := w.plan(now, true)
	if len(plan) != 0 || !active {
		t.Fatal("completed range should retain only its reader", plan, active)
	}
	if _, retry, err := w.reconcile(plan, now, changed, active); err != nil || retry || w.f.preempted {
		t.Fatal("finished range canceled its live reader", err, retry, w.f.preempted)
	}
	release()
	plan, changed, active = w.plan(now, true)
	if len(plan) != 0 || !active {
		t.Fatal("client close lost readable handoff usage or restarted completed block", plan, active)
	}
	s.mu.Lock()
	now = s.useHandoffs[0].until
	s.mu.Unlock()
	plan, changed, active = w.plan(now, true)
	if _, retry, err := w.reconcile(plan, now, changed, active); err != nil || retry || !w.f.preempted {
		t.Fatal("expired handoff retained an idle session", err, retry, w.f.preempted)
	}
}

func TestInitialRangeTaskStaysSharedWhenAnotherGETArrives(t *testing.T) {
	w, s := testSession(t)
	add := func(block int64) *downloadDemand {
		p := &playbackClock{at: time.Now(), offset: block * rangeBlockSize, limit: (block + 1) * rangeBlockSize, media: mediaTiming{w.v.size, time.Minute}}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, p), w.v, true)
		t.Cleanup(release)
		return d
	}
	add(14)
	w.jobs[14] = &scheduledBlock{} // The initial request is already waiting for headers.
	add(2)
	plan, _, _ := w.plan(time.Now(), false)
	if len(plan) != 1 || plan[14].need.Owner == "" {
		t.Fatal("concurrent GET opened another initial request", plan)
	}
	plan, _, _ = w.plan(time.Now(), true)
	if len(plan) != 2 || plan[2].need.Owner == "" || plan[14].need.Owner == "" {
		t.Fatal("initialized GETs did not regain their own ranges", plan)
	}

}

func TestColdHTTPGETOnlyDownloadsItsRange(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), int(20*rangeBlockSize/16))
	for _, tc := range []struct {
		name, header, validator string
		start, end              int64
		first, last             int64
		status                  int
		sequential              bool
	}{
		{"bounded_seek", fmt.Sprintf("bytes=%d-%d", 14*rangeBlockSize+17, 15*rangeBlockSize+23), "", 14*rangeBlockSize + 17, 15*rangeBlockSize + 24, 14, 15, 206, false},
		{"boundary", fmt.Sprintf("bytes=%d-%d", 14*rangeBlockSize, 15*rangeBlockSize-1), "", 14 * rangeBlockSize, 15 * rangeBlockSize, 14, 14, 206, false},
		{"open", fmt.Sprintf("bytes=%d-", 14*rangeBlockSize), "", 14 * rangeBlockSize, int64(len(body)), 14, 19, 206, false},
		{"suffix", "bytes=-29", "", int64(len(body)) - 29, int64(len(body)), 19, 19, 206, false},
		{"matched", fmt.Sprintf("bytes=%d-%d", 14*rangeBlockSize, 14*rangeBlockSize+7), "match", 14 * rangeBlockSize, 14*rangeBlockSize + 8, 14, 14, 206, false},
		{"mismatched", fmt.Sprintf("bytes=%d-", 14*rangeBlockSize), `"other"`, 0, int64(len(body)), 0, 19, 200, false},
		{"sequential", fmt.Sprintf("bytes=%d-%d", 14*rangeBlockSize, 14*rangeBlockSize+7), "", 14 * rangeBlockSize, 14*rangeBlockSize + 8, 14, 14, 206, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []int64
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				calls = append(calls, start/rangeBlockSize)
				mu.Unlock()
				if tc.sequential {
					w.Write(body)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				w.Header().Set("ETag", `"stable"`)
				w.WriteHeader(http.StatusPartialContent)
				w.Write(body[start : end+1])
			})
			target := videoURL(string(body))
			r, _ := http.NewRequest(http.MethodGet, target, nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			validator := tc.validator
			if validator == "match" {
				validator = `"` + v.key + `"`
			}
			response := request(s, http.MethodGet, target, map[string]string{"Range": tc.header, "If-Range": validator})
			if response.Code != tc.status || !bytes.Equal(response.Body.Bytes(), body[tc.start:tc.end]) {
				t.Fatal("incorrect range response", response.Code, response.Body.Len())
			}
			if !tc.sequential && tc.status == http.StatusPartialContent {
				repeated := request(s, http.MethodGet, target, map[string]string{"Range": tc.header, "If-Range": validator})
				if repeated.Code != tc.status || !bytes.Equal(repeated.Body.Bytes(), body[tc.start:tc.end]) {
					t.Fatal("completed range was not reusable", repeated.Code, repeated.Body.Len())
				}
			}
			s.Close()
			mu.Lock()
			got := append([]int64(nil), calls...)
			mu.Unlock()
			if len(got) == 0 || got[0] != tc.first {
				t.Fatal("initial request fetched an unrelated prefix", got)
			}
			slices.Sort(got)
			var want []int64
			for block := tc.first; block <= tc.last; block++ {
				want = append(want, block)
			}
			if !slices.Equal(got, want) {
				t.Fatal("downloaded outside GET or repeated a block", got, want)
			}
			if !tc.sequential && tc.status == 206 {
				if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
					t.Fatal("partial GET published an incomplete file", err)
				}
			}
		})
	}
}
