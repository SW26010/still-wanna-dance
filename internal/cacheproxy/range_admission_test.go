package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

type yieldingRangeBody struct {
	ctx       context.Context
	prefix    []byte
	block     int64
	ready     chan<- int64
	read      bool
	closed    chan<- int64
	closeOnce sync.Once
}

func (r *yieldingRangeBody) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	if !r.read {
		r.read = true
		r.ready <- r.block
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *yieldingRangeBody) Close() error {
	r.closeOnce.Do(func() {
		if r.closed != nil {
			r.closed <- r.block
		}
	})
	return nil
}

type gatedFirstRange struct {
	ctx  context.Context
	gate <-chan struct{}
	io.Reader
}

func (r *gatedFirstRange) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	case <-r.gate:
		return r.Reader.Read(p)
	}
}
func (r *gatedFirstRange) Close() error { return nil }

// A prefix has arrived, but the same HTTP response remains open until released.
type continuingRangeBody struct {
	ctx                  context.Context
	prefix               []byte
	remainder            io.Reader
	block                int64
	ready, closed        chan<- int64
	gate                 <-chan struct{}
	readyOnce, closeOnce sync.Once
}

func (b *continuingRangeBody) Read(p []byte) (int, error) {
	if len(b.prefix) > 0 {
		n := copy(p, b.prefix)
		b.prefix = b.prefix[n:]
		return n, nil
	}
	b.readyOnce.Do(func() { b.ready <- b.block })
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.gate:
		return b.remainder.Read(p)
	}
}
func (b *continuingRangeBody) Close() error {
	b.closeOnce.Do(func() { b.closed <- b.block })
	return nil
}

func TestBlockingReadsKeepInflightRanges(t *testing.T) {
	for _, seek := range []bool{false, true} {
		name := "first_read"
		if seek {
			name = "seek"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := setup(t, nil)
			fixedBlockScheduler(s)
			body := bytes.Repeat([]byte("0123456789abcdef"), int(10*rangeBlockSize/16))
			target, backgrounds := int64(0), int64(3)
			if seek {
				target, backgrounds = 8, 4
			}
			ready, closed := make(chan int64, 8), make(chan int64, 128)
			readGate, backgroundGate := make(chan struct{}), make(chan struct{})
			releaseRead := sync.OnceFunc(func() { close(readGate) })
			releaseBackground := sync.OnceFunc(func() { close(backgroundGate) })
			defer releaseRead()
			defer releaseBackground()
			var mu sync.Mutex
			requests := make(map[int64]int)
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					var start, end int64
					fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					block := start / rangeBlockSize
					mu.Lock()
					requests[start]++
					mu.Unlock()
					resp := &http.Response{StatusCode: 206, ContentLength: end - start + 1, Request: r,
						Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
						Body:   io.NopCloser(bytes.NewReader(body[start : end+1]))}
					if block >= 1 && block <= backgrounds {
						resp.Body = &continuingRangeBody{ctx: r.Context(), prefix: body[start : start+64*1024], remainder: bytes.NewReader(body[start+64*1024 : end+1]), block: block, ready: ready, closed: closed, gate: backgroundGate}
					} else if block == target {
						resp.Body = &gatedFirstRange{ctx: r.Context(), gate: readGate, Reader: bytes.NewReader(body[start : end+1])}
					}
					return resp, nil
				})}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			f, _, err := s.obtainMode(ctx, v, true)
			if err != nil {
				t.Fatal(err)
			}
			for range backgrounds {
				receiveDemandEvent(t, ready)
			}
			waiting := make(chan struct{})
			signalWait := sync.OnceFunc(func() { close(waiting) })
			demand := f.spool.demand
			f.spool.demand = func(offset int64) func() { release := demand(offset); signalWait(); return release }
			reader := f.spool.reader(ctx, v.size)
			defer reader.Close()
			if seek {
				reader.Seek(target*rangeBlockSize, io.SeekStart)
			}
			read := make(chan error, 1)
			go func() { _, err := io.ReadFull(reader, make([]byte, 1)); read <- err }()
			receiveDemandEvent(t, waiting)
			// Hold real Blocking beyond the former 200 ms competition threshold.
			select {
			case block := <-closed:
				t.Fatal("Blocking canceled an existing response", block)
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("blocked read did not remain active")
			}
			mu.Lock()
			targetRequests := requests[target*rangeBlockSize]
			mu.Unlock()
			if targetRequests != 1 {
				t.Fatal("Blocking was delayed or duplicated", targetRequests)
			}
			releaseRead()
			if err := receiveDemandEvent(t, read); err != nil {
				t.Fatal(err)
			}
			reader.Close()
			select {
			case block := <-closed:
				t.Fatal("Blocking removal canceled a background response", block)
			default:
			}
			releaseBackground()
			receiveDemandEvent(t, f.done)
			if f.err != nil {
				t.Fatal(f.err)
			}
			got, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal("download did not verify and publish complete bytes", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 10 {
				t.Fatal("partial ranges were re-requested", requests)
			}
			for block := int64(0); block < 10; block++ {
				if requests[block*rangeBlockSize] != 1 {
					t.Fatal("in-flight response was restarted", requests)
				}
			}
		})
	}
}
