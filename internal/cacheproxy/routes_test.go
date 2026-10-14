package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type resourceTransportFunc func(*http.Request) (*http.Response, error)

func (f resourceTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResourceDownloadUsesMeasuredChannelsWithoutChangingURL(t *testing.T) {
	for _, count := range []int{2, 5, 6} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			s, _ := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unmeasured transport used") })
			target := videoURL(payload)
			var called []int
			s.cfg.ResourceTransports = func(got string) []http.RoundTripper {
				if got != target {
					t.Errorf("address changed: %s", got)
				}
				var transports []http.RoundTripper
				for i := 0; i < count; i++ {
					transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						called = append(called, i)
						if i < count-1 {
							return nil, errors.New("selected channel failed")
						}
						if r.URL.String() != target || r.Header.Get("Range") != "bytes=0-35" {
							t.Error("resource request changed", r)
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), ContentLength: int64(len(payload)), Request: r}, nil
					}))
				}
				return transports
			}
			if _, err := s.PrefetchSong(context.Background(), "42", target); err != nil {
				t.Fatal(err)
			}
			if len(called) != count || called[0] != 0 || called[count-1] != count-1 {
				t.Fatal(called)
			}
		})
	}
}

func TestResourceFallbackIsBounded(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unmeasured fallback") })
	calls := 0
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		var out []http.RoundTripper
		for range 10 {
			out = append(out, resourceTransportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("offline") }))
		}
		return out
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err == nil {
		t.Fatal("expected failure")
	}
	if calls != upstreamrequest.MaxOperationCandidates {
		t.Fatal("unbounded fallback", calls)
	}
}

func TestResourceResumeSkipsFailedChannelWhileNextAttemptQueued(t *testing.T) {
	for _, failure := range []string{"transport", "status"} {
		t.Run(failure, func(t *testing.T) {
			s := &Server{client: &http.Client{}, requests: newRequestBudget(1, 1)}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			transferCtx, yield := context.WithCancelCause(ctx)
			defer yield(nil)
			started, fail := make(chan struct{}), make(chan struct{})
			var calls [2]int
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls[0]++
						if calls[0] == 1 {
							close(started)
							select {
							case <-fail:
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
						}
						if failure == "transport" {
							return nil, errors.New("controlled channel failure")
						}
						return &http.Response{StatusCode: 503, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
					}),
					resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls[1]++
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
					}),
				}
			}
			r, _ := http.NewRequestWithContext(transferCtx, "GET", videoURL(payload), nil)
			attempts := new(resourceAttempts)
			done := make(chan error, 1)
			go func() {
				resp, err := s.resourceResponse(r, attempts)
				if resp != nil {
					resp.Body.Close()
				}
				done <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first candidate did not start")
			}
			// An urgent request takes the slot released by the failed channel,
			// leaving the next candidate queued when the range yields to Seek.
			gotSlot := make(chan func(), 1)
			go func() { release, _ := s.requests.acquire(urgentRequestContext(ctx)); gotSlot <- release }()
			waitRequestQueue(t, s.requests, 1)
			close(fail)
			release := <-gotSlot
			if release == nil {
				t.Fatal("urgent request did not acquire released slot")
			}
			defer release()
			waitRequestQueue(t, s.requests, 1)
			yield(errRangeYielded)
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("queued attempt did not yield: %v", err)
			}
			attempts.resume()
			release()
			resp, err := s.resourceResponse(r.Clone(ctx), attempts)
			if err != nil {
				t.Fatal("next candidate did not resume", err)
			}
			resp.Body.Close()
			if calls != [2]int{1, 1} {
				t.Fatalf("resume revisited failed channel: calls=%v", calls)
			}
		})
	}
}

func TestResourceResumeRetriesChannelYieldedBeforeHeaders(t *testing.T) {
	for _, cause := range []error{errRangeYielded, errPreempted} {
		t.Run(cause.Error(), func(t *testing.T) { testResourceResumeBeforeHeaders(t, cause) })
	}
}

func testResourceResumeBeforeHeaders(t *testing.T, cause error) {
	s := &Server{client: &http.Client{}, requests: newRequestBudget(1, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	transferCtx, yield := context.WithCancelCause(ctx)
	defer yield(nil)
	started := make(chan struct{})
	var calls [2]int
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		var transports []http.RoundTripper
		for i := range 2 {
			transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls[i]++
				if i == 0 && calls[i] == 1 {
					close(started)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
			}))
		}
		return transports
	}
	r, _ := http.NewRequestWithContext(transferCtx, "GET", videoURL(payload), nil)
	attempts := new(resourceAttempts)
	done := make(chan error, 1)
	go func() {
		resp, err := s.resourceResponse(r, attempts)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first candidate did not start")
	}
	yield(cause)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active attempt did not yield: %v", err)
	}
	attempts.resume()
	resp, err := s.resourceResponse(r.Clone(ctx), attempts)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != [2]int{2, 0} {
		t.Fatalf("local yield abandoned its channel: calls=%v", calls)
	}
}
