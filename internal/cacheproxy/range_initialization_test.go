package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewGrowthDoesNotWaitForResponseHeaders(t *testing.T) {
	const blockSize = 1024
	body := bytes.Repeat([]byte("p"), 20*blockSize)
	headers, started := make(chan struct{}), make(chan struct{}, 20)
	release := sync.OnceFunc(func() { close(headers) })
	defer release()
	s, _ := setup(t, nil, func(c *Config) {
		c.BlockSize = blockSize
		c.DownloadConcurrencySoftLimit = 16
		c.DownloadGrowInterval = 10 * time.Millisecond
		c.ResolvePlayback = func(context.Context, string) (string, error) { return videoURL(string(body)), nil }
	})
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			started <- struct{}{}
			select {
			case <-headers:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: 206, ContentLength: end - start + 1, Request: r,
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
				Body: io.NopCloser(publishReaderFunc(func([]byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				}))}, nil
		})}
	}
	target := seedPreview(t, s, string(body))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServePreview(httptest.NewRecorder(), httptest.NewRequest("GET", "http://127.0.0.1/media/preview", nil).WithContext(ctx), target)
	}()
	defer func() { cancel(); <-done }()
	for range 16 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("preview did not grow to 16 while all response headers remained blocked")
		}
	}
	t.Log("held all response headers: active=16")
}

func TestConcurrentRangeEntityIsCheckedBeforeWriting(t *testing.T) {
	const blockSize = 1024
	body := strings.Repeat("e", 2*blockSize)
	prefix, entity := make(chan struct{}), make(chan struct{})
	s, _ := setup(t, nil, func(c *Config) { c.BlockSize = blockSize; c.DownloadGrowInterval = 0 })
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			response := fixtureRangeResponse(r, body)
			if r.Header.Get("Range") == "bytes=0-1023" {
				close(prefix)
				select {
				case <-entity:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				response.Header.Set("ETag", `"old"`)
			} else {
				select {
				case <-prefix:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				response.Header.Set("ETag", `"current"`)
				reader := response.Body
				var once sync.Once
				response.Body = io.NopCloser(publishReaderFunc(func(p []byte) (int, error) { once.Do(func() { close(entity) }); return reader.Read(p) }))
			}
			return response, nil
		})}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err := s.Prefetch(ctx, videoURL(body))
	if !errors.Is(err, errUpstreamDownload) || !strings.Contains(err.Error(), "entity changed") {
		t.Fatal("mismatched in-flight entity was accepted", err)
	}
	v := parsedVideo(t, s, body)
	if _, err := os.Stat(s.cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("published inconsistent entities", err)
	}
	local, err := s.acquireLocalResource(v)
	if err != nil {
		t.Fatal(err)
	}
	if local != nil {
		defer local.storage.release()
		if local.has(0) {
			t.Fatal("stored block before entity validation")
		}
	}
}

func TestUnsupportedRangeWarnsWithoutReadingWholeResponse(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			s, logs := loggingServer(t, nil)
			var reads, closes, calls atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				transports := []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, ContentLength: int64(len(payload)),
						Body: &rangeCountedBody{ReadCloser: io.NopCloser(publishReaderFunc(func([]byte) (int, error) { reads.Add(1); return 0, io.EOF })), closed: func() { closes.Add(1) }}}, nil
				})}
				if fallback {
					transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) { return fixtureRangeResponse(r, payload), nil }))
				}
				return transports
			}
			_, err := s.Prefetch(t.Context(), videoURL(payload)+"&token=private-token")
			if (err == nil) != fallback || reads.Load() != 0 || closes.Load() != 1 || calls.Load() != 1 || s.StorageError() != nil {
				t.Fatalf("unsupported response: fallback=%v err=%v reads=%d closes=%d calls=%d", fallback, err, reads.Load(), closes.Load(), calls.Load())
			}
			warned := false
			for _, record := range logs.records(t) {
				if record["event_name"] == "range_unsupported" && record["level"] == "WARN" {
					warned = true
				}
				if strings.Contains(fmt.Sprint(record), "private-token") {
					t.Fatal("warning exposed signed URL")
				}
			}
			if !warned {
				t.Fatal("missing unsupported Range warning")
			}
		})
	}
}
