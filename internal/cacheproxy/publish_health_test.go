package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type publishReaderFunc func([]byte) (int, error)

func (f publishReaderFunc) Read(p []byte) (int, error) { return f(p) }

type publishTransport struct{ body io.Reader }

func (t publishTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(t.body), ContentLength: int64(len(payload)), Request: r}, nil
}

func TestPublicationErrorsOnlyPenalizeUpstreamFailures(t *testing.T) {
	for _, mode := range []string{"create", "write", "rename", "read", "short", "checksum", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, func(http.ResponseWriter, *http.Request) {})
			r, _ := http.NewRequest("GET", videoURL(payload), nil)
			v, err := s.parse(r)
			if err != nil {
				t.Fatal(err)
			}
			f := &flight{log: cfg.Logger, streaming: make(chan struct{})}
			f.progress = startProgress(cfg.Logger, v.size, time.Hour)
			defer func() {
				f.progress.finish(err)
				if f.spool != nil {
					f.spool.finish(err)
					f.spool.release()
				}
			}()
			var body io.Reader = strings.NewReader(payload)
			readFailure := errors.New("upstream connection reset")
			switch mode {
			case "create":
				// Replace the empty temporary directory with a file.
				if err := os.Remove(s.cfg.tempDir()); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.cfg.tempDir(), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "write", "rename":
				first := true
				reader := body
				body = publishReaderFunc(func(p []byte) (int, error) {
					if first {
						first = false
						if mode == "write" {
							if err := f.spool.file.Close(); err != nil {
								t.Fatal(err)
							}
						} else {
							// Introduce a directory only after the cache-hit check, so
							// the final atomic rename (not cache cleanup) fails.
							if err := os.Mkdir(s.cfg.videoFile(v.key), 0700); err != nil {
								t.Fatal(err)
							}
						}
					}
					return reader.Read(p)
				})
			case "read":
				body = publishReaderFunc(func([]byte) (int, error) { return 0, readFailure })
			case "short":
				body = strings.NewReader(payload[:3])
			case "checksum":
				body = strings.NewReader(strings.Repeat("x", len(payload)))
			case "canceled":
				body = publishReaderFunc(func([]byte) (int, error) { return 0, context.Canceled })
			}
			s.client.Transport = publishTransport{body}
			s.noteRoute(v.host, 20*time.Millisecond, false)
			before := s.routeHealth[v.host]
			_, _, err = s.prepare(context.Background(), v, f)
			if err == nil {
				t.Fatal("expected publication failure")
			}
			wantFailure := mode == "read" || mode == "short" || mode == "checksum"
			after := s.routeHealth[v.host]
			if after.failed != wantFailure {
				t.Fatalf("mode=%s route=%+v error=%v", mode, after, err)
			}
			if !wantFailure && after != before {
				t.Fatal("local/canceled operation changed route score")
			}
			if mode == "read" && !errors.Is(err, readFailure) {
				t.Fatal("lost underlying reader error", err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation", err)
			}
		})
	}
}
