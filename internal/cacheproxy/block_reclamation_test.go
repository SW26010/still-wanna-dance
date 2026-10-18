package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func reclamationResource(t *testing.T, s *Server, v video) *localResource {
	t.Helper()
	d := &localResource{video: v, storage: newBlockFiles(t.TempDir(), v.size, nil)}
	s.localResources = map[string]*localResource{v.key: d}
	t.Cleanup(s.closeLocalResources)
	for block := int64(0); block < blockCount(v.size); block++ {
		if err := d.storage.store(block, bytes.Repeat([]byte{byte(block)}, int(blockBytes(v.size, block)))); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestBlockReclamationOnlyZeroBudgetAndUnprotectedBlocks(t *testing.T) {
	for _, mode := range []string{"zero", "positive-full", "unlimited"} {
		t.Run(mode, func(t *testing.T) {
			w, s := testSession(t)
			w.v.size = 3 * rangeBlockSize
			d := reclamationResource(t, s, w.v)
			now := time.Now()
			s.protection = map[string]map[int64]blockProtection{w.v.key: {
				0: {start: now}, 1: {until: now.Add(-time.Second)}, 2: {start: now.Add(time.Hour)},
			}}
			if mode == "positive-full" {
				s.cfg.MaxCacheBytes = 1
				s.retainedBytes = 1
			}
			s.cfg.CacheUnlimited = mode == "unlimited"
			paths := []string{localBlockPath(t, d.storage, 0), localBlockPath(t, d.storage, 1), localBlockPath(t, d.storage, 2)}
			s.reclaimLocalBlocks()
			for block, path := range paths {
				want := mode != "zero" || block == 0
				_, err := os.Stat(path)
				if d.has(int64(block)) != want || (err == nil) != want || err != nil && !os.IsNotExist(err) {
					t.Fatalf("block %d: available=%v file=%v want=%v", block, d.has(int64(block)), err, want)
				}
			}
			if mode == "zero" {
				clear(s.protection)
				s.reclaimLocalBlocks()
				if len(s.localResources) != 0 {
					t.Fatal("empty resource without execution/read references survived")
				}
			}
		})
	}
}

func TestBlockReclamationKeepsExactPinnedFileUntilReadEnds(t *testing.T) {
	w, s := testSession(t)
	w.v.size = rangeBlockSize
	d := reclamationResource(t, s, w.v)
	d.storage.retain() // An execution can still submit a replacement block.
	defer d.storage.release()
	old, err := d.storage.pin(0)
	if err != nil {
		t.Fatal(err)
	}
	unpin := sync.OnceFunc(func() { d.storage.unpin(old) })
	defer unpin()
	s.reclaimLocalBlocks()
	if d.has(0) || s.localResources[w.v.key] != d {
		t.Fatal("reclamation kept availability or detached an active writer")
	}
	if _, err := os.Stat(old.path); err != nil {
		t.Fatal("in-progress read lost its file", err)
	}
	if err := d.storage.store(0, bytes.Repeat([]byte{1}, int(w.v.size))); err != nil {
		t.Fatal(err)
	}
	fresh := localBlockPath(t, d.storage, 0)
	if fresh == old.path {
		t.Fatal("replacement reused a retired path")
	}
	data := make([]byte, 1)
	if _, err := readBlockFile(old, data, 0); err != nil || data[0] != 0 {
		t.Fatal("old read switched to replacement data", err)
	}
	unpin()
	if _, err := os.Stat(old.path); !os.IsNotExist(err) {
		t.Fatal("last I/O reference did not reclaim retired file", err)
	}
	if _, err := d.storage.ReadAt(data, 0); err != nil || data[0] != 1 {
		t.Fatal("old cleanup deleted replacement", err)
	}
}

func TestBlockReclamationFollowsGETConsumptionWithReaderStillOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.v.size = 2 * rangeBlockSize
		local := reclamationResource(t, s, w.v)
		ctx := context.WithValue(context.Background(), httpGETKey{}, true)
		demand, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		sp := local.makeSpool(nil)
		reader := sp.reader(ctx, w.v.size)
		sp.release()
		defer reader.Close()
		g := &getConsumption{s: s, demand: demand, size: w.v.size}
		g.start(http.StatusOK, nil)
		g.accept(rangeBlockSize)
		ended := time.Now()
		time.Sleep(59 * time.Second)
		s.reclaimLocalBlocks()
		if !local.has(0) || protectionFor(s, w.v.key, 0).until != ended.Add(time.Minute) {
			t.Fatal("consumed block did not keep its fixed tail")
		}
		time.Sleep(time.Second)
		s.reclaimLocalBlocks()
		if local.has(0) || !local.has(1) {
			t.Fatal("reader-wide reference prevented block cleanup or unread block lost protection")
		}
		reader.Seek(rangeBlockSize, io.SeekStart)
		data := make([]byte, 1)
		if _, err := reader.Read(data); err != nil || data[0] != 1 {
			t.Fatal("remaining reader lost protected data", err)
		}
	})
}

func TestZeroBudgetRangePreparationDoesNotRefillReclaimedBlocks(t *testing.T) {
	s, cfg := setup(t, nil)
	setRetentionLimit(t, s, 0)
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1})
	body := bytes.Repeat([]byte("x"), int(4*rangeBlockSize))
	var calls [4]atomic.Int32
	var reclaimed atomic.Bool
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			block := start / rangeBlockSize
			if calls[block].Add(1) != 1 {
				return nil, fmt.Errorf("refilled block %d", block)
			}
			if block > 0 {
				s.reclaimLocalBlocks()
				s.retentionMu.Lock()
				local := s.localResources[videoKey(string(body))]
				if local != nil && !local.has(block-1) {
					reclaimed.Store(true)
				}
				s.retentionMu.Unlock()
			}
			return &http.Response{StatusCode: 206, Request: r, ContentLength: end - start + 1,
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
				Body:   io.NopCloser(bytes.NewReader(body[start : end+1]))}, nil
		})}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Prefetch(ctx, videoURL(string(body))); !errors.Is(err, ErrNotRetained) {
		t.Fatal("reclaimed range preparation did not finish normally", err)
	}
	for block := range calls {
		if calls[block].Load() != 1 {
			t.Fatal("wrong block execution count", block, calls[block].Load())
		}
	}
	if !reclaimed.Load() {
		t.Fatal("fixture never reclaimed while download was active")
	}
	s.wg.Wait()
	s.runRetention(false)
	assertNoPartial(t, cfg.tempDir())
}

func TestReclaimedCompletedBlockOnlyRestartsForActualRead(t *testing.T) {
	w, s := testSession(t)
	w.finished = map[int64]bool{0: true}
	_, release := addTestDemand(t, s, context.Background(), w.v, false)
	defer release()
	plan, _, _ := w.plan(time.Now(), true)
	if _, ok := plan[0]; ok {
		t.Fatal("unprotected reclaimed block was scheduled again")
	}
	endRead := w.demand(0)
	plan, _, _ = w.plan(time.Now(), true)
	if !plan[0].need.Blocking {
		t.Fatal("real read could not request its missing data")
	}
	endRead()
	plan, _, _ = w.plan(time.Now(), true)
	if _, ok := plan[0]; ok {
		t.Fatal("ended Read left a synthetic refill request")
	}
}

func TestBlockReclamationWorkerExpiresTailWithReaderStillOpen(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(2*rangeBlockSize))
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	setRetentionLimit(t, s, 0)
	assertResponse(t, request(s, "GET", videoURL(string(body)), nil), 200, string(body))
	s.wg.Wait()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	reader, err := s.localBlockReader(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	s.retentionMu.Lock()
	local := s.localResources[v.key]
	s.retentionMu.Unlock()
	first, second := localBlockPath(t, local.storage, 0), localBlockPath(t, local.storage, 1)
	// Exact 60-second timing is covered by the virtual-clock test above. Here
	// shorten only the tail to exercise the real background worker and timer.
	s.retentionMu.Lock()
	p := s.protection[v.key][0]
	p.until = time.Now().Add(30 * time.Millisecond)
	s.protection[v.key][0] = p
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	expectRetained(t, first, false)
	expectRetained(t, second, true)
	reader.Seek(rangeBlockSize, io.SeekStart)
	data := make([]byte, 1)
	if _, err := reader.Read(data); err != nil || data[0] != 'x' {
		t.Fatal("background reclamation broke the remaining read", err)
	}
}
