package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanceledSeekReleasesWorkerWithoutCancelingSharedReaders(t *testing.T) {
	body := bytes.Repeat([]byte("z"), int(rangeBlockSize*4))
	prefix, oldStarted, oldCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var oldRequests atomic.Int32
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		if start == 0 {
			select {
			case <-prefix:
			case <-r.Context().Done():
				return
			}
		}
		if start == 2*rangeBlockSize && oldRequests.Add(1) == 1 {
			close(oldStarted)
			<-r.Context().Done()
			close(oldCanceled)
			return
		}
		w.Write(body[start : end+1])
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	f, one, err := s.obtain(ctx, v)
	if err != nil || one == nil {
		t.Fatalf("reader: %v", err)
	}
	defer one.Close()
	two := f.spool.reader(ctx, v.size)
	defer two.Close()
	registered := make(chan struct{}, 2)
	f.spool.mu.Lock()
	original := f.spool.demand
	f.spool.demand = func(offset int64) func() {
		release := original(offset)
		if offset == 2*rangeBlockSize {
			registered <- struct{}{}
		}
		return release
	}
	f.spool.mu.Unlock()
	done := make(chan error, 2)
	for _, reader := range []*spoolReader{one, two} {
		reader.Seek(2*rangeBlockSize, io.SeekStart)
		go func() { _, err := reader.Read(make([]byte, 8)); done <- err }()
	}
	for range 2 {
		select {
		case <-registered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case <-oldStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	one.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed reader succeeded")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-oldCanceled:
		t.Fatal("other reader lost its range")
	default:
	}
	two.Close()
	select {
	case <-oldCanceled:
	case <-ctx.Done():
		t.Fatal("orphan range was not canceled")
	}
	three := f.spool.reader(ctx, v.size)
	defer three.Close()
	three.Seek(3*rangeBlockSize, io.SeekStart)
	got := make([]byte, 8)
	if _, err := io.ReadFull(three, got); err != nil || !bytes.Equal(got, body[:8]) {
		t.Fatalf("new seek stalled: %v", err)
	}
	close(prefix)
	select {
	case <-f.done:
		if f.err != nil {
			t.Fatal(f.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if oldRequests.Load() != 2 {
		t.Fatalf("abandoned hole was not refilled: %d", oldRequests.Load())
	}
}
