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
	"time"
)

func TestStoppedSessionReusesOnlyCompleteBlocks(t *testing.T) {
	for _, mode := range []string{"valid", "grace", "weak", "changed-entity", "wrong-range", "corrupt", "foreground", "shutdown"} {
		t.Run(mode, func(t *testing.T) { testWholeFlightReuse(t, mode) })
	}
}

func testWholeFlightReuse(t *testing.T, mode string) {
	s, cfg := setup(t, nil)
	fixedBlockScheduler(s)
	body := bytes.Repeat([]byte("0123456789abcdef"), int(4*rangeBlockSize/16))
	for i := range body {
		body[i] = byte((i + i/131071) % 251)
	}
	key, target := videoKey(string(body)), videoURL(string(body))
	ready := make(chan int64, 2)
	foregroundStarted, foregroundGate := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(foregroundGate) })
	defer unblock()
	var mu sync.Mutex
	retrying := false
	starts := make(map[int64]int)
	failedChannel, redirects := 0, 0
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		var transports []http.RoundTripper
		for channel := range 2 {
			transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("e") != key {
					mu.Lock()
					retrying = true
					mu.Unlock()
					close(foregroundStarted)
					return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Request: r,
						Body: &gatedFirstRange{ctx: r.Context(), gate: foregroundGate, Reader: bytes.NewReader([]byte(payload))}}, nil
				}
				var start, end int64
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
					return nil, err
				}
				mu.Lock()
				if start/rangeBlockSize == 2 && channel == 0 {
					failedChannel++
					mu.Unlock()
					return nil, errors.New("controlled first-channel failure")
				}
				if start/rangeBlockSize == 3 && r.URL.Query().Get("endpoint") == "" {
					redirects++
					mu.Unlock()
					return &http.Response{StatusCode: 307, Header: http.Header{"Location": {r.URL.String() + "&endpoint=B"}}, Body: http.NoBody, Request: r}, nil
				}
				starts[start]++
				first := !retrying
				mu.Unlock()
				resp := &http.Response{StatusCode: 206, Header: http.Header{
					"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}, "Etag": {`"stable"`},
				}, ContentLength: end - start + 1, Request: r, Body: io.NopCloser(bytes.NewReader(body[start : end+1]))}
				if mode == "weak" {
					resp.Header.Set("ETag", `W/"stable"`)
				}
				if !first {
					condition := `"stable"`
					if mode == "weak" {
						condition = ""
					}
					if r.Header.Get("If-Match") != condition {
						t.Error("resume lost its entity condition")
					}
					switch mode {
					case "changed-entity":
						resp.Header.Set("ETag", `"changed"`)
					case "wrong-range":
						resp.Header.Set("Content-Range", "bytes 0-1/2")
					case "corrupt":
						resp.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("!"), int(end-start+1))))
					}
				}
				if first && start >= 2*rangeBlockSize {
					resp.Body = &yieldingRangeBody{ctx: r.Context(), prefix: body[start : start+64*1024], block: start / rangeBlockSize, ready: ready}
				}
				return resp, nil
			}))
		}
		return transports
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	r, _ := http.NewRequest("GET", target, nil)
	v, _ := s.parseResolved(r)
	if _, _, err := s.obtainMode(ctx, v, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		receiveDemandEvent(t, ready)
	}
	s.mu.Lock()
	old := s.flights[key]
	s.mu.Unlock()
	if old == nil {
		t.Fatal("missing partial flight")
	}
	// Wait for the two complete blocks to reach disk, independently of the
	// other workers that have already read their partial prefixes.
	for {
		old.spool.mu.Lock()
		changed := old.spool.changed
		old.spool.mu.Unlock()
		if old.spool.blocks.available(0) == 2*rangeBlockSize {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("completed blocks did not reach disk")
		}
	}
	old.progress.mu.Lock()
	received := old.progress.bytes
	old.progress.mu.Unlock()
	if received != 2*rangeBlockSize+128*1024 {
		t.Fatalf("received %d bytes before preemption", received)
	}
	partial := localBlockPath(t, old.spool.blocks, 0)
	if mode == "grace" {
		assertResponse(t, request(s, "GET", target, map[string]string{"Range": "bytes=0-2"}), http.StatusPartialContent, string(body[:3]))
	}

	s.mu.Lock()
	old.preempted = true
	old.cancel(errPreempted)
	s.mu.Unlock()
	foregroundDone := make(chan *responseRecorder, 1)
	go func() { foregroundDone <- request(s, "GET", videoURL(payload), nil) }()
	receiveDemandEvent(t, foregroundStarted)
	receiveDemandEvent(t, old.done)
	if !errors.Is(old.err, errPreempted) {
		t.Fatal("old flight did not lose its seat", old.err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("preemption removed the partial file", err)
	}
	s.retentionMu.Lock()
	checkpoint := s.localResources[key]
	s.retentionMu.Unlock()
	if checkpoint == nil || checkpoint.bytes() != 2*rangeBlockSize {
		t.Fatal("complete local blocks lost")
	}
	for block := int64(2); block < 4; block++ {
		if checkpoint.has(block) {
			t.Fatal("incomplete block became readable", block)
		}
	}
	if _, err := os.Stat(cfg.videoFile(key)); !os.IsNotExist(err) {
		t.Fatal("partial content published", err)
	}

	unblock()
	assertResponse(t, receiveDemandEvent(t, foregroundDone), 200, payload)
	s.wg.Wait() // The foreground response can precede its own spool cleanup.
	if mode == "shutdown" {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(partial); !os.IsNotExist(err) {
			t.Fatal("block file survived shutdown")
		}
		assertNoPartial(t, cfg.tempDir())
		if len(s.localResources) != 0 {
			t.Fatal("shutdown retained local resources")
		}
		return
	}
	if mode == "foreground" {
		assertResponse(t, request(s, "GET", target, nil), 200, string(body))
	} else {
		go func() { _, err := s.Prefetch(ctx, target); finished <- err }()
		err := receiveDemandEvent(t, finished)
		if mode == "changed-entity" || mode == "wrong-range" || mode == "corrupt" {
			if err == nil {
				t.Fatal("invalid resumed content accepted")
			}
			if _, err := os.Stat(cfg.videoFile(key)); !os.IsNotExist(err) {
				t.Fatal("invalid resumed content published", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			assertNoPartial(t, cfg.tempDir())
			return
		}
		if err != nil {
			t.Fatal("prefetch did not resume", err)
		}
	}
	s.wg.Wait() // Independently await persistence after foreground delivery.
	got, err := os.ReadFile(cfg.videoFile(key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("retried content differs", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 4 {
		t.Fatal("unexpected request offsets", starts)
	}
	for block := int64(0); block < 4; block++ {
		want := 1
		if block >= 2 {
			want = 2
		}
		if starts[block*rangeBlockSize] != want {
			t.Fatal("complete block reused or incomplete block restarted incorrectly", starts)
		}
	}
	if failedChannel != 2 || redirects != 2 {
		t.Fatalf("new task reused an old request cursor: %d, %d", failedChannel, redirects)
	}
	assertNoPartial(t, cfg.tempDir())
}

func TestRepeatedStopRestartsIncompleteBlockAndOldReaderReleasesSafely(t *testing.T) {
	testRepeatedPreemption(t, 64*1024, 3)
}

func TestPreemptionDoesNotTrustFullBlockWithoutEOF(t *testing.T) {
	testRepeatedPreemption(t, rangeBlockSize, 1)
}

func testRepeatedPreemption(t *testing.T, prefixSize int64, rounds int) {
	s, cfg := setup(t, nil)
	body := make([]byte, rangeBlockSize)
	for i := range body {
		body[i] = byte((i + i/32767) % 251)
	}
	ready := make(chan int64, 1)
	var finishing atomic.Bool
	var mu sync.Mutex
	var starts []int64
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			mu.Lock()
			starts = append(starts, start)
			mu.Unlock()
			resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}}, ContentLength: end - start + 1, Request: r,
				Body: io.NopCloser(bytes.NewReader(body[start : end+1]))}
			if !finishing.Load() {
				resp.Body = &yieldingRangeBody{ctx: r.Context(), prefix: body[start : start+prefixSize], ready: ready}
			}
			return resp, nil
		})}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	var oldReader *spoolReader
	for round := range rounds {
		f, _, err := s.obtainMode(ctx, v, true)
		if err != nil {
			t.Fatal(err)
		}
		receiveDemandEvent(t, ready)
		if round == 0 {
			readerCtx, stop := context.WithCancel(ctx)
			oldReader = f.spool.reader(readerCtx, v.size)
			stop()
			defer oldReader.Close()
		}
		s.mu.Lock()
		f.preempted = true
		f.cancel(errPreempted)
		preempted := true
		s.mu.Unlock()
		if !preempted {
			t.Fatal("flight did not yield")
		}
		receiveDemandEvent(t, f.done)
		if !errors.Is(f.err, errPreempted) {
			t.Fatal(f.err)
		}
		s.mu.Lock()
		occupied := len(s.flights)
		s.mu.Unlock()
		s.retentionMu.Lock()
		checkpoint := s.localResources[v.key]
		s.retentionMu.Unlock()
		if checkpoint != nil || f.ranges.bytes() != 0 || occupied != 0 {
			t.Fatal("stop retained incomplete data or held a slot")
		}

		s.requests.mu.Lock()
		active := s.requests.active
		s.requests.mu.Unlock()
		if active != 0 {
			t.Fatal("pause held HTTP quota")
		}
	}
	finishing.Store(true)
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	if reader != nil {
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("resumed foreground bytes differ", err)
		}
	}
	receiveDemandEvent(t, f.done)
	if f.err != nil {
		t.Fatal(f.err)
	}
	if got, err := os.ReadFile(cfg.videoFile(v.key)); err != nil || !bytes.Equal(got, body) {
		t.Fatal("published bytes differ", err)
	}
	oldReader.Close()
	assertNoPartial(t, cfg.tempDir())
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != rounds+1 {
		t.Fatal("unexpected retry", starts)
	}
	for _, start := range starts {
		if start != 0 {
			t.Fatal("new task reused an incomplete prefix", starts)
		}
	}
}
