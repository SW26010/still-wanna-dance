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

func TestFirstReaderWaitYieldsExistingPrefetch(t *testing.T) {
	s, _ := setup(t, nil)
	fixedBlockScheduler(s)
	body := bytes.Repeat([]byte("x"), int(5*rangeBlockSize))
	ready, closed := make(chan int64, 2), make(chan int64, 2)
	gate := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	defer unblock()
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}}, ContentLength: end - start + 1, Request: r, Body: io.NopCloser(bytes.NewReader(body[start : end+1]))}
			if start == 0 {
				resp.Body = &gatedFirstRange{ctx: r.Context(), gate: gate, Reader: bytes.NewReader(body[start : end+1])}
			}
			if start == rangeBlockSize || start == 2*rangeBlockSize {
				resp.Body = &yieldingRangeBody{ctx: r.Context(), prefix: body[start : start+64*1024], block: start / rangeBlockSize, ready: ready, closed: closed}
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
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("prefetch did not fan out")
		}
	}
	reader := f.spool.reader(ctx, v.size)
	defer reader.Close()
	read := make(chan error, 1)
	go func() { _, err := io.ReadFull(reader, make([]byte, 1)); read <- err }()
	for range 2 {
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("existing prefetch did not yield to first read")
		}
	}
	unblock()
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	reader.Close()
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal("prefetch did not resume")
	}
	if f.err != nil {
		t.Fatal(f.err)
	}
}

func TestSeekYieldsAndResumesPartialBlocksOnTheirExistingChannels(t *testing.T) {
	for _, result := range []string{"valid", "redirect", "changed-entity", "wrong-range"} {
		t.Run(result, func(t *testing.T) {
			s, _ := setup(t, nil)
			fixedBlockScheduler(s)
			body := bytes.Repeat([]byte("0123456789abcdef"), int(9*rangeBlockSize/16))
			ready := make(chan int64, 3)
			closed := make(chan int64, 3)
			seekGate := make(chan struct{})
			go func() {
				for range 3 {
					<-closed
				}
				close(seekGate)
			}()
			var mu sync.Mutex
			attempted := make(map[int64]int)
			badChannelCalls, redirects := 0, 0
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				var transports []http.RoundTripper
				for channel := range 2 {
					transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						var start, end int64
						fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
						block := start / rangeBlockSize
						mu.Lock()
						if result == "redirect" && block == 1 && r.URL.Query().Get("endpoint") == "" {
							redirects++
							mu.Unlock()
							return &http.Response{StatusCode: 307, Header: http.Header{"Location": {r.URL.String() + "&endpoint=B"}}, Body: http.NoBody, Request: r}, nil
						}
						if block == 1 && channel == 0 {
							badChannelCalls++
							mu.Unlock()
							return nil, errors.New("first channel unavailable for block one")
						}
						attempted[start]++
						mu.Unlock()
						resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}, "Etag": {`"fixed"`}}, ContentLength: end - start + 1, Request: r, Body: io.NopCloser(bytes.NewReader(body[start : end+1]))}
						if start > 0 && r.Header.Get("If-Match") != `"fixed"` {
							t.Error("resume lost entity condition")
						}
						if block >= 1 && block <= 3 {
							if start%rangeBlockSize == 0 {
								resp.Body = &yieldingRangeBody{ctx: r.Context(), prefix: body[start : start+64*1024], block: block, ready: ready, closed: closed}
							} else {
								if start%rangeBlockSize != 64*1024 {
									t.Errorf("unexpected resume offset %d", start)
								}
								if result == "changed-entity" {
									resp.Header.Set("ETag", `"changed"`)
								}
								if result == "wrong-range" {
									resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", block*rangeBlockSize, end, len(body)))
								}
							}
						}
						if block == 7 {
							resp.Body = &gatedFirstRange{ctx: r.Context(), gate: seekGate, Reader: bytes.NewReader(body[start : end+1])}
						}
						return resp, nil
					}))
				}
				return transports
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
			v, _ := s.parseResolved(r)
			f, reader, err := s.obtain(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("background blocks did not start")
				}
			}
			reader.Seek(7*rangeBlockSize, io.SeekStart)
			if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
				t.Fatal("seek was not served", err)
			}
			reader.Close()
			select {
			case <-f.done:
			case <-ctx.Done():
				t.Fatal("paused workers did not resume")
			}
			if result == "changed-entity" || result == "wrong-range" {
				if f.err == nil {
					t.Fatal("invalid resumed response accepted")
				}
				if _, err := os.Stat(s.cfg.videoFile(v.key)); !os.IsNotExist(err) {
					t.Fatal("invalid resumed content published")
				}
				return
			}
			if f.err != nil {
				t.Fatal(f.err)
			}
			got, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal("resumed bytes differ", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if badChannelCalls != 1 {
				t.Fatalf("yield revisited failed channel %d times", badChannelCalls)
			}
			if result == "redirect" && redirects != 1 {
				t.Fatalf("resume revisited old endpoint %d times", redirects)
			}
			for block := int64(1); block <= 3; block++ {
				if attempted[block*rangeBlockSize] != 1 || attempted[block*rangeBlockSize+64*1024] != 1 {
					t.Fatal("partial prefix was downloaded again", attempted)
				}
			}
		})
	}
}
