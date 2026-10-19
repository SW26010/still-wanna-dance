package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSequentialResponseZeroBudgetProtectionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		protect time.Duration
		missing bool
	}{
		{name: "outside window reclaimed", protect: 2 * time.Minute, missing: true},
		{name: "all blocks protected", protect: 4 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(4*rangeBlockSize))
			var calls atomic.Int32
			cfg := fixtureConfig()
			cfg.StorageDir = t.TempDir()
			cfg.CacheUnlimited = false
			cfg.PrepareAhead, cfg.LocalProtectAhead = 2*time.Minute, tc.protect
			cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: http.StatusOK, Request: r,
						ContentLength: int64(len(body)), Header: make(http.Header),
						Body: io.NopCloser(bytes.NewReader(body))}, nil
				})}
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			r, _ := http.NewRequest(http.MethodGet, videoURL(string(body)), nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, httpGETKey{}, true)
			ctx = context.WithValue(ctx, playbackClockKey{}, &playbackClock{
				at: time.Now(), limit: v.size, media: mediaTiming{v.size, 4 * time.Minute},
			})
			f, reader, err := s.obtain(ctx, v)
			if err != nil || reader == nil {
				t.Fatalf("obtain: reader=%v err=%v", reader != nil, err)
			}
			defer reader.Close()
			receiveDemandEvent(t, f.done)
			// Let the sequential body run ahead of the consumer. Zero-budget
			// reclamation must keep its normal window even for an active GET.
			s.reclaimLocalBlocks()
			got, err := io.ReadAll(reader)
			if tc.missing {
				if !errors.Is(err, ErrNotRetained) || !strings.Contains(err.Error(), "requested block 3 is unavailable") {
					t.Fatal("missing block did not fail explicitly", err)
				}
				if !bytes.Equal(got, body[:3*rangeBlockSize]) {
					t.Fatal("unexpected readable prefix", len(got))
				}
			} else if err != nil || !bytes.Equal(got, body) {
				t.Fatal("protected sequential response failed", len(got), err)
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected automatic whole-body retry", calls.Load())
			}
		})
	}
}

func TestStoppedSequentialResponseReusesBlocksWithFreshExecution(t *testing.T) {
	for _, ranged := range []bool{false, true} {
		t.Run(fmt.Sprint("next_range=", ranged), func(t *testing.T) {
			s, cfg := setup(t, nil)
			body := bytes.Repeat([]byte("x"), int(2*rangeBlockSize))
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			ready := make(chan int64, 1)
			var next atomic.Bool
			var calls atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					resp := &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: v.size, Request: r}
					if !next.Load() {
						resp.Body = &yieldingRangeBody{ctx: r.Context(), prefix: body[:rangeBlockSize+64*1024], ready: ready}
					} else if ranged {
						var start, end int64
						fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
						if start != rangeBlockSize {
							t.Error("complete first block downloaded again", start)
						}
						resp.StatusCode = 206
						resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, v.size))
						resp.ContentLength = end - start + 1
						resp.Body = io.NopCloser(bytes.NewReader(body[start : end+1]))
					} else {
						resp.Body = io.NopCloser(bytes.NewReader(body))
					}
					return resp, nil
				})}
			}
			old, _, err := s.obtainMode(context.Background(), v, true)
			if err != nil {
				t.Fatal(err)
			}
			receiveDemandEvent(t, ready)
			old.cancel(errPreempted)
			receiveDemandEvent(t, old.done)
			if old.ranges.bytes() != rangeBlockSize || old.ranges.has(1) {
				t.Fatal("stopped sequential response retained incomplete bytes")
			}
			next.Store(true)
			if _, err := s.Prefetch(context.Background(), videoURL(string(body))); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("unexpected fresh execution count", calls.Load())
			}
			assertNoPartial(t, cfg.tempDir())
			assertResponse(t, request(s, "GET", videoURL(string(body)), nil), 200, string(body))
		})
	}
}
