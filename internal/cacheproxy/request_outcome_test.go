package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// Exercise the HTTP exit, persisted result and read-only schema validation,
// including cancellation caused by sending the terminal response.
func TestRequestTerminalFailureHistory(t *testing.T) {
	for _, afterBody := range []bool{false, true} {
		for _, reason := range []string{"expired", "timeout", "upstream-status"} {
			t.Run(fmt.Sprintf("%s/body=%v", reason, afterBody), func(t *testing.T) {
				s, cfg := setup(t, nil)
				body := strings.Repeat("x", int(rangeBlockSize+16))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				firstBody := make(chan struct{})
				signal := sync.OnceFunc(func() { close(firstBody) })
				defer signal()
				s.cfg.ResourceTransports = func(string) []http.RoundTripper {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						if afterBody && r.Header.Get("Range") == fmt.Sprintf("bytes=0-%d", rangeBlockSize-1) {
							return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", rangeBlockSize-1, len(body))}}, ContentLength: rangeBlockSize, Body: io.NopCloser(strings.NewReader(body[:rangeBlockSize])), Request: r}, nil
						}
						if afterBody {
							select {
							case <-firstBody:
							case <-r.Context().Done():
								return nil, context.Cause(r.Context())
							}
						}
						switch reason {
						case "expired":
							return nil, blocksched.ErrExpired
						case "timeout":
							return nil, context.DeadlineExceeded
						default:
							return &http.Response{StatusCode: 504, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("gateway timeout")), Request: r}, nil
						}
					})}
				}
				w := &outcomeWriter{ResponseRecorder: httptest.NewRecorder(), afterWrite: signal}
				if !afterBody {
					w.afterWrite = cancel // Service reason must survive a later disconnect.
				}
				var crash any
				func() {
					defer func() { crash = recover() }()
					s.ServeHTTP(w, httptest.NewRequest("GET", videoURL(body), nil).WithContext(ctx))
				}()
				if afterBody && (crash != http.ErrAbortHandler || w.Body.Len() == 0) || !afterBody && crash != nil {
					t.Fatalf("crash=%v bytes=%d", crash, w.Body.Len())
				}
				wantStatus := 504
				if reason == "upstream-status" {
					wantStatus = 502
				}
				if afterBody {
					wantStatus = 200
				}
				if w.Code != wantStatus {
					t.Fatalf("status=%d, want %d", w.Code, wantStatus)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
				if err != nil || len(page.Requests) != 1 {
					t.Fatal(page, err)
				}
				want := reason
				if reason != "expired" {
					want = "aborted"
					if !afterBody {
						want = "failed"
					}
				}
				if page.Requests[0].Outcome != want {
					t.Fatalf("got %+v, want %s", page.Requests[0], want)
				}
			})
		}
	}
}

func TestPlaybackResolutionFailureHistory(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		err          error
		status       int
	}{
		{"timeout", "", context.DeadlineExceeded, 504},
		{"upstream", "", errors.New("upstream unavailable"), 502},
		{"invalid URL", "://invalid", nil, 502},
		{"missing metadata", "https://video.example/no-resource-metadata", nil, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := setup(t, nil, func(c *Config) {
				c.ResolvePlayback = func(context.Context, string) (string, error) { return tc.target, tc.err }
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &outcomeWriter{ResponseRecorder: httptest.NewRecorder(), afterWrite: cancel}
			s.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
			if w.Code != tc.status {
				t.Fatal(w.Code)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
			if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "playback_info_failed" {
				t.Fatal(page, err)
			}
		})
	}
}

func TestPlaybackResolutionStorageFailureHistory(t *testing.T) {
	s, cfg := setup(t, nil, func(c *Config) {
		c.ResolvePlayback = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("address write: %w", ErrLocalStorage)
		}
	})
	if w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "failed" {
		t.Fatal(page, err)
	}
}

type outcomeWriter struct {
	*httptest.ResponseRecorder
	afterWrite func()
}

func (w *outcomeWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.afterWrite()
	return n, err
}

func TestBlockedHistoryKeepsRequestTimePolicy(t *testing.T) {
	s, cfg := setup(t, nil)
	if err := s.Store.SetSongPolicy(t.Context(), 42, SongBlocked); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if err := s.Store.SetSongPolicy(t.Context(), 42, SongNormal); err != nil {
		t.Fatal(err)
	}
	// Existing generic history must remain generic, even with the same HTTP code.
	if err := s.Store.usage.write([]usageEvent{{at: time.Now().UnixMilli(), method: "GET", outcome: "failed", status: 403, songID: "42"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 2 {
		t.Fatal(page, err)
	}
	seen := map[string]bool{}
	for _, r := range page.Requests {
		seen[r.Outcome] = true
	}
	if !seen["blocked"] || !seen["failed"] {
		t.Fatal(page)
	}
}

func TestRequestRetrySuccessDoesNotKeepTimeout(t *testing.T) {
	s, cfg := setup(t, nil)
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{
			resourceTransportFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded }),
			resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				return fixtureRangeResponse(r, payload), nil
			}),
		}
	}
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "completed" {
		t.Fatal(page, err)
	}
}

func TestLocalPlaybackHistoryIgnoresBackgroundLookupFailure(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { serveFixtureContent(w, r, payload) })
	if _, err := s.PrefetchSong(t.Context(), "42", videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	s.cfg.ResolvePlayback = func(context.Context, string) (string, error) { return "", context.DeadlineExceeded }
	assertResponse(t, request(s, "GET", "http://api.udon.dance/Api/Songs/play?id=42", nil), 200, payload)
	s.wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "completed" {
		t.Fatal(page, err)
	}
}

func TestRequestHistoryReopenPreservesLegacyResults(t *testing.T) {
	s, cfg := setup(t, nil)
	var events []usageEvent
	for i, outcome := range []string{"completed", "canceled", "failed", "aborted", "incomplete", "timeout", "expired", "blocked", "", "future_result"} {
		events = append(events, usageEvent{at: time.Now().UnixMilli() + int64(i), method: "GET", outcome: outcome, status: 504})
	}
	if err := s.Store.usage.write(events); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || len(before.Requests) != len(events) {
		t.Fatal(before, err)
	}
	reopenFixtureStore(t, &cfg)
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 50})
	if err != nil || !reflect.DeepEqual(before.Requests, after.Requests) {
		t.Fatal("history changed on reopen", before, after, err)
	}
}

func TestRequestCancellationAndKnownFailureOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := &responseWriter{}
	w.recordFailure(ctx, errors.Join(context.DeadlineExceeded, context.Canceled))
	if got := w.outcome(ctx, http.ErrAbortHandler); got != "canceled" {
		t.Fatal(got)
	}
	for _, err := range []error{blocksched.ErrExpired, context.DeadlineExceeded, ErrSongBlocked, errPlaybackUpstream, ErrLocalStorage} {
		w := &responseWriter{}
		w.recordFailure(context.Background(), err)
		before := w.outcome(context.Background(), http.ErrAbortHandler)
		if got := w.outcome(ctx, http.ErrAbortHandler); got != before {
			t.Fatal(before, got)
		}
		w.recordFailure(ctx, context.Canceled)
		if got := w.outcome(ctx, http.ErrAbortHandler); got != before {
			t.Fatal("later failure replaced result", before, got)
		}
	}
}
