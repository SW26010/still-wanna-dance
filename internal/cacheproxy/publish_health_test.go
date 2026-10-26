package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type publishReaderFunc func([]byte) (int, error)

func (f publishReaderFunc) Read(p []byte) (int, error) { return f(p) }

type publishTransport struct{ body io.Reader }

func (t publishTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(t.body), ContentLength: int64(len(payload)), Request: r}, nil
}

func TestPublicationDistinguishesLocalAndUpstreamFailures(t *testing.T) {
	for _, mode := range []string{"create", "write", "rename", "read", "short", "checksum", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := setup(t, func(http.ResponseWriter, *http.Request) {}, func(cfg *Config) { fixtureFaultableBlocks(t, cfg) })
			r, _ := http.NewRequest("GET", videoURL(payload), nil)
			v, err := s.parse(r)
			if err != nil {
				t.Fatal(err)
			}
			f := &flight{log: cfg.Logger, streaming: make(chan struct{})}
			f.progress = startProgress(cfg.Logger, v.size, time.Hour)
			defer func() {
				f.progress.finish(err)
				if f.ranges != nil {
					f.ranges.storage.release()
				}
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
							if err := os.Remove(s.cfg.tempDir()); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(s.cfg.tempDir(), nil, 0600); err != nil {
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
			_, release := addTestDemand(t, s, context.Background(), v, false)
			defer release()
			_, _, err = s.prepare(context.Background(), v, f)
			if err == nil {
				t.Fatal("expected publication failure")
			}
			wantFailure := mode == "read" || mode == "short" || mode == "checksum"
			if (errors.Is(err, errUpstreamDownload) && !errors.Is(err, context.Canceled)) != wantFailure {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			wantLocal := mode == "create" || mode == "write" || mode == "rename"
			if errors.Is(err, ErrLocalStorage) != wantLocal {
				t.Fatalf("local classification mode=%s err=%v", mode, err)
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

func TestSequentialStopDoesNotRetainIncompleteBlock(t *testing.T) {
	s, cfg := setup(t, nil)
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	f := &flight{log: cfg.Logger, streaming: make(chan struct{})}
	f.progress = startProgress(cfg.Logger, v.size, time.Hour)
	defer func() {
		f.progress.finish(nil)
		if f.spool != nil {
			f.spool.release()
		}
		if f.ranges != nil {
			f.ranges.storage.release()
		}
	}()
	var requests atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			body := publishReaderFunc(func(p []byte) (int, error) {
				cancel(errPreempted)
				<-r.Context().Done()
				return copy(p, payload[:3]), context.Canceled
			})
			return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: v.size, Body: io.NopCloser(body), Request: r}, nil
		})}
	}
	_, release := addTestDemand(t, s, context.Background(), v, false)
	defer release()
	_, _, err := s.prepare(ctx, v, f)
	if !errors.Is(err, errPreempted) || s.StorageError() != nil || requests.Load() != 1 {
		t.Fatal("stop became a storage fault or retried", err, s.StorageError())
	}
	if f.ranges.bytes() != 0 {
		t.Fatal("incomplete sequential block became readable")
	}
	assertNoPartial(t, cfg.tempDir())
}
