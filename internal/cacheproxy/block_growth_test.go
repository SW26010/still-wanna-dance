package cacheproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// Exercise the same queue, real missing GET reads and observation used by the
// UI. Hold all upstream bodies so completed/reused slots cannot obscure growth.
func TestBlockObservationQueueGrowthAndGETBypass(t *testing.T) {
	body := strings.Repeat("g", int(16*rangeBlockSize))
	type start struct {
		at time.Time
		e  blocksched.Event
	}
	started := make(chan start, 32)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		last = min(last, int64(len(body)-1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(last-first+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, func(cfg *Config) {
		cfg.DownloadGrowInterval = time.Second
		cfg.DownloadConcurrencySoftLimit = 16
		cfg.ObserveBlockSchedule = func(e blocksched.Event) {
			if e.Kind == "started" {
				started <- start{time.Now(), e}
			}
		}
	})
	sub, err := s.ObserveBlocks()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := s.UpdatePrefetchQueue([]int64{1344}, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prefetched := make(chan struct{})
	go func() {
		defer close(prefetched)
		s.PrefetchSong(s.queueDemandContext(ctx, "1344"), "1344", videoURL(body))
	}()
	nextStart := func() start {
		t.Helper()
		select {
		case e := <-started:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("missing block start")
			return start{}
		}
	}
	waitView := func(predicate func(*BlockSnapshot) bool) *BlockSnapshot {
		t.Helper()
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()
		for {
			view, changed, closed := sub.Snapshot()
			if view != nil && predicate(view) {
				return view
			}
			if closed {
				t.Fatal("block observation closed")
			}
			select {
			case <-changed:
			case <-timeout.C:
				t.Fatal("missing block observation", view)
			}
		}
	}
	var first, previous time.Time
	for i := 1; i <= 4; i++ {
		current := nextStart()
		if i == 1 {
			first = current.at
		} else if current.at.Sub(previous) < time.Second-10*time.Millisecond {
			t.Fatalf("ordinary capacity grew early: %v", current.at.Sub(previous))
		}
		if current.e.Blocking || current.e.Active != i || current.e.Open != i {
			t.Fatalf("ordinary queue admission: %+v", current.e)
		}
		t.Logf("ordinary: elapsed=%v active=%d open=%d", current.at.Sub(first), current.e.Active, current.e.Open)
		previous = current.at
	}
	waitView(func(v *BlockSnapshot) bool {
		downloading := 0
		for _, b := range v.Blocks {
			if b.State == "downloading" {
				downloading++
			}
		}
		return downloading == v.Active && v.Active >= 4
	})

	// Different readers seek into still-missing blocks of the same queued video.
	// Their real Read waits may start together despite the one-second interval.
	getCtx, cancelGET := context.WithCancel(ctx)
	defer cancelGET()
	getDone := make(chan struct{}, 3)
	for _, block := range []int64{12, 13, 14} {
		go func() {
			defer func() { getDone <- struct{}{} }()
			defer func() {
				if recovered := recover(); recovered != nil && recovered != http.ErrAbortHandler {
					panic(recovered)
				}
			}()
			r := httptest.NewRequest("GET", videoURL(body), nil).WithContext(getCtx)
			r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", block*rangeBlockSize, (block+1)*rangeBlockSize-1))
			s.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	blocked := 0
	for blocked < 3 {
		current := nextStart()
		if current.e.Blocking {
			if current.e.Active <= current.e.Open {
				t.Fatalf("missing GET waited for ordinary capacity: %+v", current.e)
			}
			blocked++
			t.Logf("GET: elapsed=%v active=%d open=%d", current.at.Sub(first), current.e.Active, current.e.Open)
		}
	}
	waitView(func(v *BlockSnapshot) bool {
		waiting, downloading := 0, 0
		for _, b := range v.Blocks {
			if b.Readers > 0 && b.State == "downloading" {
				waiting++
			}
			if b.State == "downloading" {
				downloading++
			}
		}
		return waiting == 3 && downloading == v.Active && v.Active >= 7
	})
	cancelGET()
	for range 3 {
		select {
		case <-getDone:
		case <-time.After(5 * time.Second):
			t.Fatal("GET did not cancel")
		}
	}
	view := waitView(func(v *BlockSnapshot) bool {
		retained := 0
		for _, b := range v.Blocks {
			if b.Readers > 0 {
				return false
			}
			if b.Block >= 12 && b.Block <= 14 && b.State == "downloading" {
				// Wait out GET handoffs too, so only the independent queue
				// source can account for keeping these executions alive.
				sources := v.Details[b.ID].Sources
				if len(sources) != 1 || sources[0].Kind != "queue" {
					return false
				}
				retained++
			}
		}
		return retained == 3
	})
	t.Logf("GET released; queue retains running blocks: active=%d open=%d", view.Active, view.Open)
	cancel()
	select {
	case <-prefetched:
	case <-time.After(5 * time.Second):
		t.Fatal("queue preparation did not cancel")
	}
	// The queue owns its usage independently of the preparation call. Closing
	// the engine in setup cleanup cancels the gated upstream bodies.
}
