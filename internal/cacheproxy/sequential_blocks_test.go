package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

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
