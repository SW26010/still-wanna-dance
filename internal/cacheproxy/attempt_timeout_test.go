package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

type delayedResourceBody struct {
	io.ReadCloser
	ctx   context.Context
	delay time.Duration
}

func (b *delayedResourceBody) Read(p []byte) (int, error) {
	if b.delay > 0 {
		delay := b.delay
		b.delay = 0
		select {
		case <-time.After(delay):
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.ReadCloser.Read(p)
}

func TestResourceHeaderTimeoutLeavesTimeForFallback(t *testing.T) {
	for _, count := range []int{2, 6} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &Server{Store: &Store{}, cfg: DefaultConfig(), client: &http.Client{}, requests: new(requestActivity)}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				start := time.Now()
				var calls []int
				s.cfg.ResourceTransports = func(string) []http.RoundTripper {
					var transports []http.RoundTripper
					for index := range count {
						transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
							calls = append(calls, index)
							if index == 0 {
								<-r.Context().Done()
								return nil, r.Context().Err()
							}
							select {
							case <-time.After(time.Second):
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
							resp := fixtureRangeResponse(r, payload)
							resp.Body = &delayedResourceBody{ReadCloser: resp.Body, ctx: r.Context(), delay: 10 * time.Second}
							return resp, nil
						}))
					}
					return transports
				}
				r, _ := http.NewRequestWithContext(ctx, "GET", videoURL(payload), nil)
				resp, err := s.resourceResponse(r, new(resourceAttempts))
				if err != nil {
					t.Fatalf("fallback could not start: calls=%v elapsed=%v error=%v", calls, time.Since(start), err)
				}
				defer resp.Body.Close()
				got, err := io.ReadAll(resp.Body)
				if err != nil || string(got) != payload {
					t.Fatalf("fallback body lost its full budget: elapsed=%v body=%q error=%v", time.Since(start), got, err)
				}
				if len(calls) != 2 || calls[0] != 0 || calls[1] != 1 || time.Since(start) != 30*time.Second/time.Duration(count)+11*time.Second {
					t.Fatalf("wrong fallback sequence or header budget: calls=%v elapsed=%v", calls, time.Since(start))
				}
			})
		})
	}
}

func TestScheduledHeaderTimeoutLeavesTimeForFallback(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		parent, moveAt, headers, body, completed time.Duration
	}{
		{name: "task", completed: 15 * time.Second},
		{name: "earlier-parent", parent: 10 * time.Second, completed: 5 * time.Second},
		{name: "move-before-fallback", moveAt: 5 * time.Second, headers: 20 * time.Second, completed: 35 * time.Second},
		{name: "move-during-body", moveAt: 20 * time.Second, body: 20 * time.Second, completed: 35 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testSessionServer(t, blocksched.Config{SoftLimit: 1})
				s.cfg = DefaultConfig()
				var calls [2]int
				s.cfg.ResourceTransports = func(string) []http.RoundTripper {
					return []http.RoundTripper{
						resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
							calls[0]++
							<-r.Context().Done()
							return nil, r.Context().Err()
						}),
						resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
							calls[1]++
							if tc.headers > 0 {
								select {
								case <-time.After(tc.headers):
								case <-r.Context().Done():
									return nil, r.Context().Err()
								}
							}
							resp := fixtureRangeResponse(r, payload)
							resp.Body = &delayedResourceBody{ReadCloser: resp.Body, ctx: r.Context(), delay: tc.body}
							return resp, nil
						}),
					}
				}
				r, _ := http.NewRequest("GET", videoURL(payload), nil)
				v, err := s.parse(r)
				if err != nil {
					t.Fatal(err)
				}
				w := testResourceSession(t, s, v)
				if tc.parent > 0 {
					ctx, cancel := context.WithTimeout(t.Context(), tc.parent)
					defer cancel()
					w.ctx = ctx
				}
				w.f.ranges.video = v
				w.f.discard, w.f.log = true, s.cfg.Logger
				w.f.progress = &downloadProgress{log: w.f.log, start: time.Now()}
				start := time.Now()
				j := new(scheduledBlock)
				need := blocksched.Need{Owner: v.key, ReadyBy: start, LeaseUntil: start.Add(time.Hour)}
				if err := w.submit(0, j, need); err != nil {
					t.Fatal(err)
				}
				if tc.moveAt > 0 {
					time.Sleep(tc.moveAt)
					need.ReadyBy = start.Add(30 * time.Second)
					if !j.merged.Update(need) {
						t.Fatal("live demand change was rejected")
					}
				}
				if err := j.task.Err(); err != nil {
					t.Fatalf("scheduled fallback could not finish: calls=%v elapsed=%v error=%v", calls, time.Since(start), err)
				}
				if calls != [2]int{1, 1} || !w.f.ranges.has(0) || w.f.progress.bytes != int64(len(payload)) || time.Since(start) != tc.completed {
					t.Fatalf("scheduled range lost its fallback budget: calls=%v elapsed=%v", calls, time.Since(start))
				}
			})
		})
	}
}

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
						if r.URL.Scheme != "http" {
							t.Fatal("resource changed protocol", r.URL)
						}
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
							return fixtureRangeResponse(r, payload), nil
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
			// The second HTTP candidate gets a fresh request deadline.
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
