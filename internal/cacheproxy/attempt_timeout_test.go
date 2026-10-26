package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestEachResourceAttemptHasAnIndependentTimeout(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := &Server{Store: &Store{}, cfg: DefaultConfig(), client: &http.Client{}, requests: new(requestActivity)}
			s.cfg.DownloadTimeout = time.Second
			start := time.Now()
			ctx := context.Background()
			if bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 1500*time.Millisecond)
				defer cancel()
			}
			calls := 0
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						deadline, _ := r.Context().Deadline()
						if deadline != start.Add(time.Second) {
							t.Error("first attempt budget was divided", deadline)
						}
						<-r.Context().Done()
						return nil, r.Context().Err()
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls++
						select {
						case <-time.After(900 * time.Millisecond):
							return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					}),
				}
			}
			r, _ := http.NewRequestWithContext(ctx, "GET", videoURL(payload), nil)
			resp, err := s.resourceResponse(r, new(resourceAttempts))
			if bounded {
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 1500*time.Millisecond {
					t.Fatal("parent deadline lost", err, time.Since(start))
				}
			} else {
				if err != nil {
					t.Fatal("retry consumed first attempt's budget", err)
				}
				defer resp.Body.Close()
				got, err := io.ReadAll(resp.Body)
				if string(got) != payload || err != nil || time.Since(start) != 1900*time.Millisecond {
					t.Fatal("retry could not finish with fresh budget", err, time.Since(start))
				}
			}
			if calls != 2 {
				t.Fatal("wrong attempt count", calls)
			}
		})
	}
}

type timeoutClosedBody struct {
	closed chan struct{}
	closes atomic.Int32
}

func (b *timeoutClosedBody) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrClosedPipe }
func (b *timeoutClosedBody) Close() error {
	if b.closes.Add(1) == 1 {
		close(b.closed)
	}
	return nil
}

func TestAttemptTimeoutClosesBodyAndReleasesBeforeRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Server{Store: &Store{}, cfg: DefaultConfig(), client: &http.Client{}, requests: new(requestActivity)}
		s.cfg.DownloadTimeout = time.Second
		body := &timeoutClosedBody{closed: make(chan struct{})}
		active := 0
		s.cfg.BeginResourceLoad = func() func() { active++; return func() { active-- } }
		s.cfg.ResourceTransports = func(string) []http.RoundTripper {
			return []http.RoundTripper{
				resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 206, Header: make(http.Header), Body: body, Request: r}, nil
				}),
				resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					if active != 1 {
						t.Error("previous attempt still owns request activity", active)
					}
					return &http.Response{StatusCode: 206, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
				}),
			}
		}
		r, _ := http.NewRequest("GET", videoURL(payload), nil)
		attempts := new(resourceAttempts)
		resp, err := s.resourceResponse(r, attempts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(resp.Body); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("body timeout did not unblock read", err)
		}
		resp.Body.Close()
		if body.closes.Load() != 1 || active != 0 {
			t.Fatal("body close/release was duplicated or omitted", body.closes.Load(), active)
		}
		resp, err = s.resourceResponse(r, attempts)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if active != 0 {
			t.Fatal("retry leaked request activity")
		}
	})
}
