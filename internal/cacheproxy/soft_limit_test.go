package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestBlockedReadersOpenHTTPBeyondSoftLimit(t *testing.T) {
	const readers = 20
	const firstReadBlock = 1
	body := bytes.Repeat([]byte("x"), int((firstReadBlock+readers)*rangeBlockSize))
	started := make(chan int64, 128)
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		started <- start / rangeBlockSize
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := s.obtainMode(ctx, v, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, readers+1)
	// Each actual read waits on a different block after the initial request.
	// A duplicate reader must share block 1 rather than create a 21st request.
	for i := range readers + 1 {
		reader := f.spool.reader(ctx, v.size)
		defer reader.Close()
		reader.Seek(int64(firstReadBlock+i%readers)*rangeBlockSize, io.SeekStart)
		go func() {
			reader.Read(make([]byte, 1))
			done <- struct{}{}
		}()
	}
	seen := make(map[int64]int)
	for len(seen) < readers {
		select {
		case block := <-started:
			if block >= firstReadBlock {
				seen[block]++
				if seen[block] != 1 {
					t.Fatal("blocked readers duplicated a shared request", block)
				}
			}
		case <-ctx.Done():
			t.Fatal("blocked HTTP requests hit a lower-layer quota", len(seen))
		}
	}
	s.requests.mu.Lock()
	active := s.requests.active
	s.requests.mu.Unlock()
	if active < readers || active <= videoRequestSoftLimit {
		t.Fatal("actual HTTP requests did not exceed the soft limit", active)
	}
	cancel()
	for range readers + 1 {
		<-done
	}
}
