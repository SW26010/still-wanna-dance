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
)

func TestRangeTransportErrorsRedactSignedURLs(t *testing.T) {
	for _, cause := range []error{errors.New("connection refused"), errors.New("TLS handshake failed")} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, logs := loggingServer(t, nil)
			body := strings.Repeat("x", int(rangeBlockSize+16))
			target := videoURL(body) + "&token=private-token&signature=private-signature"
			var failures atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", rangeBlockSize-1) {
						failures.Add(1)
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
			// A second request exercises the HTTP logging exit as well as worker logs.
			request(s, "GET", target, nil)
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
		})
	}
}
