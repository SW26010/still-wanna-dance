package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRangeFailureStatusPreservesRootCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"connection", 502}, {"timeout", 504}, {"parent-deadline", 504}, {"shutdown", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, nil)
			body := strings.Repeat("x", int(rangeBlockSize+16))
			if tc.name == "parent-deadline" {
				s.cfg.DownloadTimeout = 100 * time.Millisecond
			}
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Range") == fmt.Sprintf("bytes=0-%d", rangeBlockSize-1) {
						return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", rangeBlockSize-1, len(body))}}, ContentLength: rangeBlockSize, Body: io.NopCloser(strings.NewReader(body[:rangeBlockSize])), Request: r}, nil
					}
					switch tc.name {
					case "timeout":
						return nil, context.DeadlineExceeded
					case "parent-deadline":
						<-r.Context().Done()
						return nil, r.Context().Err()
					case "shutdown":
						s.cancel()
						<-r.Context().Done()
						return nil, r.Context().Err()
					default:
						return nil, errors.New("connection refused")
					}
				})}
			}
			// No requested bytes have been returned, so the HTTP status must
			// reflect the root failure rather than the other worker's cancellation.
			w := request(s, "GET", videoURL(body), map[string]string{"Range": "bytes=-8"})
			if w.Code != tc.status || w.aborted {
				t.Fatalf("status=%d aborted=%v body=%s", w.Code, w.aborted, w.Body.String())
			}
		})
	}
}

func TestRangeTransportErrorsRedactSignedURLs(t *testing.T) {
	for _, cause := range []error{errors.New("connection refused"), errors.New("TLS handshake failed")} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, logs := loggingServer(t, nil)
			body := strings.Repeat("x", int(rangeBlockSize+16))
			target := videoURL(body) + "&token=private-token&signature=private-signature"
			var failures atomic.Int32
			firstBody := make(chan struct{}, 1)
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", rangeBlockSize-1) {
						if failures.Add(1) == 3 {
							select {
							case <-firstBody:
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
						}
						return nil, cause // http.Client wraps this with the complete signed URL.
					}
					return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", rangeBlockSize-1, len(body))}}, ContentLength: rangeBlockSize, Body: io.NopCloser(strings.NewReader(body[:rangeBlockSize])), Request: r}, nil
				})}
			}
			r, _ := http.NewRequest("GET", target, nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			f, reader, err := s.obtain(context.Background(), v)
			if reader != nil {
				defer reader.Close()
			}
			<-f.done
			if err == nil {
				err = f.err
			}
			if !errors.Is(err, errUpstreamDownload) || !errors.Is(err, cause) {
				t.Fatalf("lost classification: %v", err)
			}
			if failures.Load() != 2 {
				t.Fatalf("range attempts=%d", failures.Load())
			}
			if strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "token=") {
				t.Fatalf("unsafe error: %v", err)
			}
			// Force the second request to send body bytes before its later range
			// fails, exercising stream_failed rather than only cache_failed.
			w := &firstBodyRecorder{httptest.NewRecorder(), firstBody}
			var aborted any
			func() {
				defer func() { aborted = recover() }()
				s.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
			}()
			if aborted != http.ErrAbortHandler || w.Body.Len() == 0 {
				t.Fatalf("abort=%v bytes=%d", aborted, w.Body.Len())
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			logs.mu.Lock()
			data := bytes.Clone(logs.data.Bytes())
			logs.mu.Unlock()
			if bytes.Contains(data, []byte("private-")) || bytes.Contains(data, []byte("token=")) || bytes.Contains(data, []byte("signature=")) {
				t.Fatalf("signed URL leaked: %s", data)
			}
			if !bytes.Contains(data, []byte(cause.Error())) {
				t.Fatal("root diagnostic missing")
			}
			if !bytes.Contains(data, []byte(`"msg":"stream_failed"`)) {
				t.Fatal("stream error exit was not exercised")
			}
		})
	}
}
