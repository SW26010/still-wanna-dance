package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestPrefetchObservationUsesNormalNonblockingLoad(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 3*int(rangeBlockSize)+17)
	cfg := DefaultConfig()
	cfg.CacheUnlimited = true // The second prefetch must reuse a retained video.
	prepareFixtureResources(t, &cfg, t.TempDir(), true)
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	var received atomic.Int64
	var mu sync.Mutex
	var events []blocksched.Event
	cfg.ObserveDownloadBytes = func(n int64) { received.Add(n) }
	cfg.ObserveBlockSchedule = func(e blocksched.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }
	cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			time.Sleep(2 * time.Millisecond)
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload))}}, ContentLength: end - start + 1, Body: io.NopCloser(bytes.NewReader(payload[start : end+1]))}, nil
		})}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		source, err := s.Prefetch(context.Background(), videoURL(string(payload)))
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && source != "HIT" {
			t.Fatalf("second request was not a normal cache hit: %s", source)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if received.Load() != int64(len(payload)) {
		t.Fatalf("observed bytes=%d want=%d", received.Load(), len(payload))
	}
	mu.Lock()
	defer mu.Unlock()
	starts, samples := 0, 0
	for _, e := range events {
		if e.Blocking {
			t.Fatalf("ordinary load became blocking: %+v", e)
		}
		if e.Kind == "started" {
			starts++
		}
		if e.Kind == "sample" {
			samples++
			if e.BlockTime <= 0 {
				t.Fatal("missing engine T")
			}
		}
	}
	if starts != 4 || samples != 3 {
		t.Fatalf("incorrect normal work observations: starts=%d samples=%d", starts, samples)
	}
}
