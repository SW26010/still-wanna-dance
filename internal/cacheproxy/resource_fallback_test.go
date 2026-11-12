package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

type stalledResourceBody struct{ ctx context.Context }

func (b stalledResourceBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b stalledResourceBody) Close() error             { return nil }

func TestResourceFallbackIncludesRangeValidationAndBody(t *testing.T) {
	for _, later := range []bool{false, true} {
		for _, failure := range []string{"wrong-range", "short", "header-timeout", "body-timeout"} {
			t.Run(fmt.Sprintf("later=%v/%s", later, failure), func(t *testing.T) {
				s, _ := setup(t, nil)
				s.cfg.DownloadTimeout = time.Second
				size := 36
				if later {
					size += int(rangeBlockSize)
				}
				body := bytes.Repeat([]byte("z"), size)
				target := videoURL(string(body))
				var calls [2]atomic.Int32
				s.cfg.ResourceTransports = func(got string) []http.RoundTripper {
					if got != target {
						t.Errorf("URL changed: %s", got)
					}
					var transports []http.RoundTripper
					for i := range 2 {
						transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
							calls[i].Add(1)
							var start, end int64
							fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
							resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, size)}}, ContentLength: end - start + 1, Request: r}
							resp.Body = io.NopCloser(contextReader{r.Context(), bytes.NewReader(body[start : end+1])})
							if i == 0 && (!later || start > 0) {
								switch failure {
								case "wrong-range":
									resp.Header.Set("Content-Range", "bytes 0-0/1")
								case "short":
									resp.Body = io.NopCloser(bytes.NewReader(body[start:end]))
								case "header-timeout":
									<-r.Context().Done()
									return nil, r.Context().Err()
								case "body-timeout":
									resp.Body = stalledResourceBody{r.Context()}
								}
							}
							return resp, nil
						}))
					}
					return transports
				}
				if _, err := s.Prefetch(context.Background(), target); err != nil {
					t.Fatal(err)
				}
				wantFirst := int32(1)
				if later {
					wantFirst = 2
				}
				if calls[0].Load() != wantFirst || calls[1].Load() != 1 {
					t.Fatalf("first=%d backup=%d", calls[0].Load(), calls[1].Load())
				}
			})
		}
	}
}

func TestResourceBodyFallbackStopsAtCandidateBudget(t *testing.T) {
	s, _ := setup(t, nil)
	var calls atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		var transports []http.RoundTripper
		for range 8 {
			transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-35/36"}}, ContentLength: 36, Body: io.NopCloser(bytes.NewReader([]byte("short"))), Request: r}, nil
			}))
		}
		return transports
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err == nil {
		t.Fatal("short content accepted")
	}
	if calls.Load() != upstreamrequest.MaxOperationCandidates {
		t.Fatalf("attempts=%d", calls.Load())
	}
}

func TestResourceBodyFallbackReachesLastCandidate(t *testing.T) {
	s, _ := setup(t, nil)
	var calls [upstreamrequest.MaxOperationCandidates]atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		var transports []http.RoundTripper
		for i := range calls {
			transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls[i].Add(1)
				body := "short"
				if i == len(calls)-1 {
					body = payload
				}
				return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-35/36"}}, ContentLength: 36, Body: io.NopCloser(bytes.NewBufferString(body)), Request: r}, nil
			}))
		}
		return transports
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err)
	}
	for i := range calls {
		if calls[i].Load() != 1 {
			t.Fatalf("candidate %d: calls=%d", i, calls[i].Load())
		}
	}
}

func TestRangeRedirectBudgetSurvivesBodyFailures(t *testing.T) {
	for _, tc := range []struct {
		name             string
		channels         int
		initialRedirects int
	}{
		{"single-channel", 1, 0},
		{"multiple-channels", 6, 0},
		{"initial-redirects", 6, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cancel context.CancelFunc
			var calls atomic.Int32
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if n >= 40 {
					cancel() // Safety stop if changing URLs still bypasses the budget.
					return
				}
				if n <= tc.initialRedirects || (n-tc.initialRedirects)%2 == 0 {
					u := *r.URL
					q := u.Query()
					q.Set("hop", strconv.Itoa(n))
					u.RawQuery = q.Encode()
					w.Header().Set("Location", "http://"+r.Host+u.RequestURI())
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.Header().Set("Content-Range", "bytes 0-35/36")
				w.WriteHeader(http.StatusPartialContent)
				io.WriteString(w, "short")
			})
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				channels := make([]http.RoundTripper, tc.channels)
				for i := range channels {
					channels[i] = s.client.Transport
				}
				return channels
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := s.Prefetch(ctx, videoURL(payload))
			// Follow five redirects, then reject the sixth. Initial redirects
			// must still count after the first response reaches the block reader.
			want := int32(12 - tc.initialRedirects)
			if ctx.Err() != nil || !errors.Is(err, errTooManyVideoRedirects) || calls.Load() != want {
				t.Fatalf("calls=%d want=%d caller_error=%v error=%v", calls.Load(), want, ctx.Err(), err)
			}
		})
	}
}

func TestLaterRangeRedirectRetriesFinalEndpoint(t *testing.T) {
	for _, failure := range []string{"short", "wrong-range", "changed-etag"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := setup(t, nil, func(c *Config) { c.DownloadGrowInterval = 0 })
			body := bytes.Repeat([]byte("z"), int(2*rangeBlockSize+16))
			original := videoURL(string(body))
			// Each cold block uses all five redirects and must still be able
			// to retry the final endpoint on its backup channel.
			final := original + "&hop=5"
			var originalFirst, originalBackup, finalFirst, finalBackup atomic.Int32
			var entered atomic.Int32
			allStarted, prefixRead := make(chan struct{}), make(chan struct{})
			s.cfg.ResourceTransports = func(target string) []http.RoundTripper {
				var transports []http.RoundTripper
				for channel := range 2 {
					transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						var start, end int64
						fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
						if target == original {
							if channel == 1 {
								originalBackup.Add(1)
								return nil, errors.New("A backup unavailable")
							}
							originalFirst.Add(1)
							// All cold tasks start before any final endpoint is known.
							if entered.Add(1) == 3 {
								close(allStarted)
							}
							select {
							case <-allStarted:
							case <-r.Context().Done():
								return nil, r.Context().Err()
							}
							return &http.Response{StatusCode: 307, Header: http.Header{"Location": {original + "&hop=1"}}, Body: http.NoBody, Request: r}, nil
						} else if target != final {
							hop, err := strconv.Atoi(r.URL.Query().Get("hop"))
							if err != nil || hop < 1 || hop >= 5 {
								t.Errorf("unexpected target %s", target)
							}
							return &http.Response{StatusCode: 307, Header: http.Header{"Location": {original + "&hop=" + strconv.Itoa(hop+1)}}, Body: http.NoBody, Request: r}, nil
						}
						resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}, "Etag": {`"stable"`}}, ContentLength: end - start + 1, Body: io.NopCloser(bytes.NewReader(body[start : end+1])), Request: r}
						if target == final {
							if channel == 0 {
								finalFirst.Add(1)
								if r.Header.Get("If-Match") != "" {
									t.Error("redirect forwarded a foreign entity condition")
								}
								if start == 0 {
									resp.Body = &rangeCountedBody{ReadCloser: resp.Body, closed: sync.OnceFunc(func() { close(prefixRead) })}
									return resp, nil
								}
								// Establish the final endpoint's entity before injecting
								// an independent body/validation failure there.
								select {
								case <-prefixRead:
								case <-r.Context().Done():
									return nil, r.Context().Err()
								}
								switch failure {
								case "short":
									resp.Body = io.NopCloser(bytes.NewReader(body[start:end]))
								case "wrong-range":
									resp.Header.Set("Content-Range", "bytes 0-0/1")
								case "changed-etag":
									resp.Header.Set("ETag", `"other"`)
								}
							} else {
								finalBackup.Add(1)
								if r.Header.Get("If-Match") != `"stable"` {
									t.Error("final endpoint retry lost its own entity condition")
								}
							}
						}
						return resp, nil
					}))
				}
				return transports
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := s.Prefetch(ctx, original); err != nil {
				t.Fatal(err)
			}
			// This initial concurrent wave starts at A; each block's retry
			// stays at B and retains B's own entity condition.
			if originalFirst.Load() != 3 || originalBackup.Load() != 0 || finalFirst.Load() != 3 || finalBackup.Load() != 2 {
				t.Fatalf("A first=%d A backup=%d B first=%d B backup=%d", originalFirst.Load(), originalBackup.Load(), finalFirst.Load(), finalBackup.Load())
			}
		})
	}
}
