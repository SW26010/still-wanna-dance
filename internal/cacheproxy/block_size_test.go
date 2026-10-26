package cacheproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestBlockSizeValidation(t *testing.T) {
	for _, size := range []int64{-1, 0, 1, 384<<10 + 17, 512 << 10, 2 << 20, int64(^uint(0) >> 1)} {
		cfg := DefaultConfig()
		cfg.VideoDir = t.TempDir()
		cfg.BlockSize = size
		valid := size >= 0 && uint64(size) < uint64(^uint(0)>>1)
		if err := cfg.validate(); (err == nil) != valid {
			t.Fatalf("block size %d: %v, valid=%v", size, err, valid)
		}
	}
}

func TestConfiguredBlockSizeHTTPAndPublication(t *testing.T) {
	for _, configured := range []int64{0, 512 << 10, 2 << 20, 384<<10 + 17} {
		for _, sequential := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/sequential=%v", configured, sequential), func(t *testing.T) {
				t.Parallel() // Different engines must not share mutable block geometry.
				blockSize := configured
				if blockSize == 0 {
					blockSize = DefaultBlockSize
				}
				body := make([]byte, 2*blockSize+123)
				for i := range body {
					body[i] = byte(i * 31 % 251)
				}
				var mu sync.Mutex
				requests := make(map[int64]int)
				samples := make(chan blocksched.Event, 8)
				s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
					var start, end int64
					if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || start%blockSize != 0 || end != min(start+blockSize, int64(len(body)))-1 {
						t.Errorf("unexpected Range %q: %v", r.Header.Get("Range"), err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					mu.Lock()
					requests[start/blockSize]++
					mu.Unlock()
					if sequential {
						w.Header().Set("Content-Length", fmt.Sprint(len(body)))
						w.Write(body)
						return
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
					w.WriteHeader(http.StatusPartialContent)
					w.Write(body[start : end+1])
				}, func(c *Config) {
					c.BlockSize = configured
					c.DownloadGrowInterval = 0
					c.ObserveBlockSchedule = func(e blocksched.Event) {
						if e.Kind == "sample" {
							samples <- e
						}
					}
				})
				if s.cfg.BlockSize != blockSize {
					t.Fatalf("effective block size %d, want %d", s.cfg.BlockSize, blockSize)
				}
				target := videoURL(string(body))
				// A cold read inside block 1 must not fetch the preceding block.
				for range 2 {
					w := request(s, "GET", target, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", blockSize+11, blockSize+39)})
					if w.aborted || w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), body[blockSize+11:blockSize+40]) {
						t.Fatalf("nonzero read: status=%d, bytes=%d, aborted=%v", w.Code, w.Body.Len(), w.aborted)
					}
					s.wg.Wait()
				}
				mu.Lock()
				initialOK := len(requests) == 1 && requests[1] == 1
				mu.Unlock()
				if !initialOK {
					t.Fatal("initial range expanded or complete data was downloaded again", requests)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := s.Prefetch(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(s.cfg.videoFile(fmt.Sprintf("%x", md5.Sum(body))))
				if err != nil || !bytes.Equal(got, body) {
					t.Fatal("complete publication did not preserve bytes", err)
				}
				// Drain worker completion before inspecting observations. The first
				// short GET can cancel its worker after the block becomes readable;
				// canceled work is deliberately excluded from comparable samples.
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				minSamples, wantSamples := 1, 2
				if sequential {
					minSamples, wantSamples = 0, 0
				}
				if len(samples) < minSamples || len(samples) > wantSamples {
					t.Fatalf("samples=%d, want=%d..%d (tail/sequential must not sample)", len(samples), minSamples, wantSamples)
				}
				for len(samples) > 0 {
					if e := <-samples; e.WorkBytes != blockSize || e.BlockTime <= 0 {
						t.Fatal("sample uses the wrong block geometry", e)
					}
				}
				// Reopen the same published library with different geometry.
				cfg.BlockSize = blockSize + 37
				reopenFixtureStore(t, &cfg)
				reopened, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				w := request(reopened, "GET", target, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", blockSize-7, blockSize+9)})
				if w.aborted || w.Code != http.StatusPartialContent || w.Header().Get("X-SWD-Cache") != "HIT" || !bytes.Equal(w.Body.Bytes(), body[blockSize-7:blockSize+10]) {
					t.Fatal("published video was not reusable after a block-size change", w.Code, w.Header())
				}
				mu.Lock()
				defer mu.Unlock()
				wantRequests := map[int64]int{0: 1, 1: 1, 2: 1}
				if sequential {
					wantRequests = map[int64]int{1: 1}
				}
				if len(requests) != len(wantRequests) {
					t.Fatal("unexpected upstream work", requests)
				}
				for block, want := range wantRequests {
					if requests[block] != want {
						t.Fatal("complete block was not reused", requests)
					}
				}
			})
		}
	}
}

func TestConfiguredBlockSizeDemandConsumptionAndObservation(t *testing.T) {
	const blockSize int64 = 384<<10 + 17
	s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
	s.cfg.BlockSize = blockSize
	v := video{key: "configured", size: 3*blockSize + 23}
	clock := &playbackClock{at: time.Now(), offset: blockSize - 3, limit: 2*blockSize + 5, media: mediaTiming{v.size, 100 * time.Second}}
	ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
	d, release := addTestDemand(t, s, ctx, v, true)
	defer release()
	// Observe actual projected demand, including byte sizes and deadlines.
	o := &blockObserver{s: s, sourceIDs: make(map[blockSource]uint64), videos: make(map[string]blockVideo)}
	view, _ := o.capture(nil)
	if view.Used != 3 || len(view.Blocks) != 3 {
		t.Fatalf("incorrect intersecting blocks: used=%d blocks=%d", view.Used, len(view.Blocks))
	}
	for i := int64(0); i < 3; i++ {
		b := view.Blocks[blockViewID(useBlockID{v.key, i})]
		if b.ByteOffset != i*blockSize || b.Bytes != blockSize || b.TotalBlocks != 4 || b.NeedBy != clock.ready(i*blockSize).UnixMilli() {
			t.Fatal("observation geometry or deadline differs from GET", b)
		}
	}
	g := &getConsumption{s: s, demand: d, size: v.size}
	g.start(http.StatusPartialContent, http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", clock.offset, clock.limit-1, v.size)}})
	for _, step := range []struct{ bytes, remaining int64 }{{3, 2}, {blockSize, 1}, {5, 0}} {
		g.accept(step.bytes)
		if int64(len(s.useBlocks)) != step.remaining {
			t.Fatalf("consumed %d: remaining=%d, want=%d", step.bytes, len(s.useBlocks), step.remaining)
		}
	}
	// Capacity remains a count of blocks, independent of the byte geometry.
	_, _, err := s.addDemand(context.Background(), video{key: "too-many", size: (backgroundUseBlocks + 1) * blockSize}, false)
	if !errors.Is(err, ErrDemandCapacity) {
		t.Fatal("background capacity no longer counts configured blocks", err)
	}
}
