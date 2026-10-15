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

func TestBlockDurationSamplesOnlyCompleteRangeExecutions(t *testing.T) {
	for _, tc := range []struct {
		name              string
		size              int64
		sequential, retry bool
		samples           int
	}{
		{"full_and_tail", rangeBlockSize + 123, false, false, 1},
		{"tail_only", 123, false, false, 0},
		{"successful_retry", rangeBlockSize, false, true, 1},
		{"sequential_same_size", rangeBlockSize, true, false, 0},
		{"sequential_whole_file", 3 * rangeBlockSize, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, nil)
			s.scheduler.Close()
			samples := make(chan blocksched.Event, 8)
			s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 4, Observe: func(e blocksched.Event) {
				if e.Kind == "sample" {
					samples <- e
				}
			}})
			body := bytes.Repeat([]byte("x"), int(tc.size))
			var calls atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					attempt := calls.Add(1)
					// Response-header waiting is part of T even though no body byte
					// has arrived; a failed attempt must not reset the admission time.
					time.Sleep(10 * time.Millisecond)
					if tc.sequential {
						return &http.Response{StatusCode: 200, ContentLength: tc.size, Header: make(http.Header), Request: r, Body: io.NopCloser(bytes.NewReader(body))}, nil
					}
					var start, end int64
					fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					data := body[start : end+1]
					if tc.retry && attempt == 1 {
						data = data[:100]
					}
					return &http.Response{StatusCode: 206, ContentLength: end - start + 1, Request: r,
						Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, tc.size)}},
						Body:   io.NopCloser(bytes.NewReader(data))}, nil
				})}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			url := videoURL(string(body))
			if _, err := s.Prefetch(ctx, url); err != nil {
				t.Fatal(err)
			}
			if len(samples) != tc.samples {
				t.Fatal("incomparable execution sampled", len(samples), tc.samples)
			}
			if tc.samples > 0 {
				e := <-samples
				minimum := 10 * time.Millisecond
				if tc.retry {
					minimum *= 2
				}
				if e.BlockTime < minimum {
					t.Fatal("header or retry time omitted", e.BlockTime)
				}
			}
			r, _ := http.NewRequest("GET", url, nil)
			v, _ := s.parseResolved(r)
			got, err := os.ReadFile(s.cfg.videoFile(v.key))
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal("sampled before valid publication", err)
			}
		})
	}
}
