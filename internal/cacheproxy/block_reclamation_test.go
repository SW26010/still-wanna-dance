package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func reclamationResource(t *testing.T, s *Server, v video) *localResource {
	t.Helper()
	d := &localResource{video: v, storage: newBlockFiles(t.TempDir(), v.size, rangeBlockSize, nil)}
	s.localResources = map[string]*localResource{v.key: d}
	t.Cleanup(s.closeLocalResources)
	for block := int64(0); block < blockCount(v.size, rangeBlockSize); block++ {
		if err := d.storage.store(block, bytes.Repeat([]byte{byte(block)}, int(blockBytes(v.size, block, rangeBlockSize)))); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestBlockReclamationPolicyAndUnprotectedBlocks(t *testing.T) {
	for _, policy := range []string{"auto", "on", "off"} {
		for _, mode := range []string{"zero", "positive-full", "unlimited"} {
			t.Run(policy+"/"+mode, func(t *testing.T) {
				w, s := testSession(t)
				s.cfg.AggressiveBlockReclamation = policy
				aggressive := policy == "on" || policy == "auto" && mode == "zero"
				w.v.size = 3 * rangeBlockSize
				d := reclamationResource(t, s, w.v)
				now := time.Now()
				s.protection = map[string]map[int64]blockProtection{w.v.key: {
					0: {start: now}, 1: {until: now.Add(-time.Second)}, 2: {start: now.Add(time.Hour)},
				}}
				if mode == "positive-full" {
					s.cfg.VideoBudgetBytes = 1
					s.retainedBytes = 1
				}
				s.cfg.CacheUnlimited = mode == "unlimited"
				paths := []string{localBlockPath(t, d.storage, 0), localBlockPath(t, d.storage, 1), localBlockPath(t, d.storage, 2)}
				s.reclaimLocalBlocks()
				for block, path := range paths {
					want := !aggressive || block == 0
					_, err := os.Stat(path)
					if d.has(int64(block)) != want || (err == nil) != want || err != nil && !os.IsNotExist(err) {
						t.Fatalf("block %d: available=%v file=%v want=%v", block, d.has(int64(block)), err, want)
					}
				}
				if aggressive {
					clear(s.protection)
					s.reclaimLocalBlocks()
					if len(s.localResources) != 0 {
						t.Fatal("empty resource without execution/read references survived")
					}
				}
			})
		}
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
		time.Sleep(s.cfg.LocalProtectionGrace - time.Second)
		s.reclaimLocalBlocks()
		if !local.has(0) || protectionFor(s, w.v.key, 0).until != ended.Add(s.cfg.LocalProtectionGrace) {
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

func TestAggressiveRangePreparationDoesNotRefillReclaimedBlocks(t *testing.T) {
	for _, mode := range []string{"zero-auto", "positive-on", "unlimited-on"} {
		t.Run(mode, func(t *testing.T) { testAggressiveRangePreparation(t, mode) })
	}
}

func testAggressiveRangePreparation(t *testing.T, mode string) {
	s, cfg := setup(t, nil)
	setRetentionLimit(t, s, 0)
	if mode != "zero-auto" {
		s.retentionMu.Lock()
		s.cfg.AggressiveBlockReclamation = "on"
		s.cfg.VideoBudgetBytes = 8 * rangeBlockSize
		s.cfg.CacheUnlimited = mode == "unlimited-on"
		s.retentionMu.Unlock()
	}
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
	if err := s.StorageError(); err != nil {
		t.Fatal("normal reclamation became a persistent storage failure", err)
	}
	s.wg.Wait()
	s.runRetention(false)
	assertNoPartial(t, cfg.tempDir())
}

func TestUnprotectedReclaimedBlockOnlyRestartsForActualRead(t *testing.T) {
	w, s := testSession(t)
	w.finished = map[int64]bool{0: true}
	_, release := addTestDemand(t, s, context.Background(), w.v, false)
	defer release()
	plan, _, _ := w.plan(time.Now(), true)
	if _, ok := plan[0]; ok {
		t.Fatal("unprotected reclaimed block was scheduled again")
	}
	endRead := w.reads.Wait(0)
	plan, _, _ = w.plan(time.Now(), true)
	if _, ok := plan[0]; !ok {
		t.Fatal("real read could not request its missing data")
	}
	endRead()
	plan, _, _ = w.plan(time.Now(), true)
	if _, ok := plan[0]; ok {
		t.Fatal("ended Read left a synthetic refill request")
	}
}

func TestReclaimedBlockReentersPlanForCurrentUsage(t *testing.T) {
	for _, policy := range []string{"auto", "on"} {
		for _, source := range []string{"GET", "queue"} {
			t.Run(policy+"/"+source, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w, s := testSession(t)
					s.cfg.AggressiveBlockReclamation = policy
					if policy == "on" {
						s.cfg.VideoBudgetBytes = 10 * rangeBlockSize
					}
					w.v.size = 2 * rangeBlockSize
					local := reclamationResource(t, s, w.v)
					w.f.ranges = local
					w.finished = map[int64]bool{0: true}
					ctx := context.WithValue(context.Background(), httpGETKey{}, true)
					oldCtx := context.WithValue(ctx, playbackClockKey{}, &playbackClock{
						at: time.Now(), limit: w.v.size, media: estimateMediaTiming(w.v.size, 120),
					})
					old, release := addTestDemand(t, s, oldCtx, w.v, true)
					defer release()
					g := &getConsumption{s: s, demand: old, size: w.v.size}
					g.start(http.StatusOK, nil)
					g.accept(rangeBlockSize)
					time.Sleep(s.cfg.LocalProtectionGrace)
					s.reclaimLocalBlocks()
					if local.has(0) || !local.has(1) {
						t.Fatal("consumed block was not reclaimed independently")
					}
					if source == "GET" {
						_, closeGET := addTestDemand(t, s, ctx, w.v, true)
						defer closeGET()
					} else {
						s.UpdatePrefetchQueue([]int64{1}, false)
						v := w.v
						v.songID = "1"
						_, closeQueue := addTestDemand(t, s, s.queueDemandContext(ctx, "1"), v, false)
						defer closeQueue()
					}
					plan, _, _ := w.plan(time.Now(), true)
					if use, ok := plan[0]; !ok || use.need.ReadyBy.IsZero() {
						t.Fatal("old completion suppressed the new finite usage", plan)
					}
					if _, ok := plan[1]; ok || len(w.reads.Requested()) != 0 {
						t.Fatal("planning redownloaded available data or synthesized a read")
					}
				})
			})
		}
	}
}

func TestReclamationOffRetainsZeroBudgetBlocksUntilShutdown(t *testing.T) {
	var calls atomic.Int32
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, payload)
	})
	setRetentionLimit(t, s, 0)
	s.retentionMu.Lock()
	s.cfg.AggressiveBlockReclamation = "off"
	s.retentionMu.Unlock()
	for range 2 {
		if _, err := s.Prefetch(context.Background(), videoURL(payload)); !errors.Is(err, ErrNotRetained) {
			t.Fatal("zero video budget allowed publication", err)
		}
		s.wg.Wait()
		s.runRetention(false)
	}
	if calls.Load() != 1 || s.StorageError() != nil {
		t.Fatal("unprotected blocks were not reused", calls.Load(), s.StorageError())
	}
	expectRetained(t, cfg.videoFile(videoKey(payload)), false)
	files, err := filepath.Glob(filepath.Join(cfg.tempDir(), "download-block-*.part"))
	if err != nil || len(files) == 0 {
		t.Fatal("block cache was not retained during this run", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestReclaimedBlockFollowsProtectionWindowAndWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		w.finished = map[int64]bool{0: true}
		_, releaseManual := addTestDemand(t, s, context.Background(), w.v, false)
		defer releaseManual()
		ctx := context.WithValue(context.Background(), httpGETKey{}, true)
		ctx = context.WithValue(ctx, playbackClockKey{}, &playbackClock{
			at: time.Now().Add(s.protectionAhead() + time.Second), limit: rangeBlockSize,
			media: estimateMediaTiming(w.v.size, 0),
		})
		_, releaseGET := addTestDemand(t, s, ctx, w.v, true)
		defer releaseGET()
		plan, _, _ := w.plan(time.Now(), true)
		if _, ok := plan[0]; ok {
			t.Fatal("future use allowed an unprotected refill")
		}
		time.Sleep(time.Second)
		plan, _, _ = w.plan(time.Now(), true)
		if _, ok := plan[0]; !ok {
			t.Fatal("entering the window did not requalify the missing block")
		}
		w.jobs[0] = &scheduledBlock{} // A refetch already in progress must also lose qualification.
		releaseGET()
		plan, _, _ = w.plan(time.Now(), true)
		if _, ok := plan[0]; ok {
			t.Fatal("withdrawn finite usage kept refilling for the untimed source")
		}
	})
}

func TestZeroBudgetRefetchBeforeReadKeepsSharedSession(t *testing.T) {
	for _, delay := range []time.Duration{0, 200 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			s, _ := setup(t, nil)
			setRetentionLimit(t, s, 0)
			s.cfg.PrepareAhead = 2 * time.Minute
			started := make(chan blocksched.Event, 8)
			s.scheduler.Close()
			s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1, PrepareAhead: s.cfg.PrepareAhead, Observe: func(e blocksched.Event) {
				if e.Kind == "started" {
					started <- e
				}
			}})
			body := bytes.Repeat([]byte("r"), int(2*rangeBlockSize))
			gate, second := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			defer unblock()
			var calls [2]atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
						return nil, err
					}
					block := start / rangeBlockSize
					calls[block].Add(1)
					var reader io.Reader = bytes.NewReader(body[start : end+1])
					if block == 1 {
						close(second)
						reader = &gatedFirstRange{ctx: r.Context(), gate: gate, Reader: reader}
					}
					return &http.Response{StatusCode: 206, Request: r, ContentLength: end - start + 1,
						Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
						Body:   io.NopCloser(reader)}, nil
				})}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, _ := http.NewRequest(http.MethodGet, videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			old, _, err := s.obtainMode(ctx, v, true)
			if err != nil {
				t.Fatal(err)
			}
			receiveDemandEvent(t, second) // First block was reaped; last block is still in flight.
			receiveDemandEvent(t, started)
			receiveDemandEvent(t, started)
			s.reclaimLocalBlocks()
			if old.ranges.has(0) {
				t.Fatal("fixture did not reclaim the completed block")
			}
			at := time.Now()
			if delay > 0 {
				at = at.Add(s.cfg.PrepareAhead + delay)
			}
			getCtx := context.WithValue(ctx, httpGETKey{}, true)
			getCtx = context.WithValue(getCtx, playbackClockKey{}, &playbackClock{
				at: at, limit: rangeBlockSize, media: estimateMediaTiming(v.size, 0),
			})
			current, reader, err := s.obtain(getCtx, v)
			if err != nil || reader == nil || current != old {
				t.Fatal("new GET did not attach to the existing session", err)
			}
			defer reader.Close()
			unblock()
			// No Read yet: the new use must refetch proactively. In the future
			// case all original work completes before the new window opens.
			var e blocksched.Event
			select {
			case e = <-started:
			case <-old.done:
				select {
				case e = <-started: // A fast refetch may already have completed.
				default:
					t.Fatal("historical completion ended the session before refetch", old.err)
				}
			case <-ctx.Done():
				t.Fatal("new usage waited for Blocking instead of refetching")
			}
			if e.Blocking || time.Now().Before(at.Add(-s.cfg.PrepareAhead)) {
				t.Fatal("refetch bypassed the ordinary window", e)
			}
			got := make([]byte, rangeBlockSize)
			if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, body[:rangeBlockSize]) {
				t.Fatal("refetched data was not readable", err)
			}
			receiveDemandEvent(t, old.done)
			if !errors.Is(old.err, ErrNotRetained) || calls[0].Load() != 2 || calls[1].Load() != 1 {
				t.Fatal("refetch repeated work or changed zero-budget completion", old.err, calls[0].Load(), calls[1].Load())
			}
		})
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
