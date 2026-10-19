package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestStoppedInitialRequestStartsWithFreshCandidates(t *testing.T) {
	for _, failure := range []string{"transport", "status", "invalid-range", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := setup(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, _ := http.NewRequest("GET", videoURL(payload), nil)
			v, err := s.parseResolved(r)
			if err != nil {
				t.Fatal(err)
			}
			f := &flight{streaming: make(chan struct{}), log: s.cfg.Logger}
			f.progress = startProgress(f.log, v.size, time.Minute)
			defer f.progress.finish(nil)
			w := newDownloadSession(s, ctx, v, f)
			defer func() {
				if f.spool != nil {
					f.spool.release()
				}
				if f.ranges != nil {
					f.ranges.storage.release()
				}
			}()
			started := make(chan struct{}, 2)
			failedCalls, currentCalls, redirects := 0, 0, 0
			s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
				if failure == "redirect" && !strings.Contains(target, "endpoint=next") {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						redirects++
						return &http.Response{StatusCode: 307, Header: http.Header{"Location": {r.URL.String() + "&endpoint=next"}}, Body: http.NoBody, Request: r}, nil
					})}
				}
				return []http.RoundTripper{
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						failedCalls++
						switch failure {
						case "status":
							return &http.Response{StatusCode: 503, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
						case "invalid-range":
							return &http.Response{StatusCode: 206, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
						default:
							return nil, errors.New("controlled first-channel failure")
						}
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						currentCalls++
						if currentCalls <= 2 {
							started <- struct{}{}
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						if r.Header.Get("Range") != "bytes=0-35" {
							t.Error("initial range changed on restart", r.Header)
						}
						return &http.Response{StatusCode: 206, ContentLength: int64(len(payload)), Request: r,
							Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload))}},
							Body:   io.NopCloser(strings.NewReader(payload))}, nil
					}),
				}
			}
			for range 2 {
				attemptCtx, stop := context.WithCancelCause(ctx)
				done := make(chan blocksched.Result, 1)
				go func() { done <- w.run(attemptCtx, new(blocksched.Progress), 0) }()
				select {
				case <-started:
				case <-ctx.Done():
					stop(nil)
					<-done
					t.Fatal("next candidate did not reach response-header wait")
				}
				stop(context.Canceled)
				result := <-done
				if !errors.Is(result.Err, context.Canceled) || f.ranges != nil {
					t.Fatal("did not stop before initialization", result.Err)
				}
			}
			if result := w.run(ctx, new(blocksched.Progress), 0); result.Err != nil {
				t.Fatal("initial request did not restart", result.Err)
			}
			got := make([]byte, len(payload))
			if _, err := f.spool.blocks.ReadAt(got, 0); err != nil || string(got) != payload || !f.ranges.has(0) {
				t.Fatal("restartd first range did not complete", err)
			}
			if failedCalls != 3 || currentCalls != 3 || failure == "redirect" && redirects != 3 {
				t.Fatalf("new execution reused old history: failed=%d current=%d redirects=%d", failedCalls, currentCalls, redirects)
			}
			s.requests.mu.Lock()
			defer s.requests.mu.Unlock()
			if s.requests.active != 0 {
				t.Fatal("stopd/completed initial attempts retained HTTP activity", s.requests.active)
			}
		})
	}
}
