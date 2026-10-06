package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestFullResponseFallbackKeepsTaskDeadline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates int
		timeout    time.Duration
		succeeds   bool
	}{
		{"single", 1, time.Second, true},
		{"with-backup", 2, time.Second, true},
		{"task-deadline", 2, 200 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				select {
				case <-time.After(700 * time.Millisecond):
					io.WriteString(w, payload)
				case <-r.Context().Done():
				}
			})
			s.cfg.DownloadTimeout = tc.timeout
			var backup atomic.Int32
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				transports := []http.RoundTripper{s.client.Transport}
				if tc.candidates > 1 {
					transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						backup.Add(1)
						return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewReader([]byte(payload))), Request: r}, nil
					}))
				}
				return transports
			}
			_, err := s.Prefetch(context.Background(), videoURL(payload))
			if tc.succeeds && err != nil {
				t.Fatal(err)
			}
			if !tc.succeeds && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("parent deadline lost: %v", err)
			}
			if backup.Load() != 0 {
				t.Fatalf("accepted full response was restarted: %d", backup.Load())
			}
		})
	}
}

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

func TestResourceBodyFallbackStopsAtFourChannels(t *testing.T) {
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
	if calls.Load() != 4 {
		t.Fatalf("attempts=%d", calls.Load())
	}
}

func TestLaterRangeRedirectRetriesFinalEndpoint(t *testing.T) {
	for _, failure := range []string{"short", "wrong-range", "changed-etag"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := setup(t, nil)
			body := bytes.Repeat([]byte("z"), int(2*rangeBlockSize+16))
			original := videoURL(string(body))
			final := original + "&endpoint=B"
			var originalFirst, originalBackup, finalFirst, finalBackup atomic.Int32
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
							if start > 0 {
								return &http.Response{StatusCode: 307, Header: http.Header{"Location": {final}}, Body: http.NoBody, Request: r}, nil
							}
						} else if target != final {
							t.Errorf("unexpected target %s", target)
						}
						resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}, "Etag": {`"stable"`}}, ContentLength: end - start + 1, Body: io.NopCloser(bytes.NewReader(body[start : end+1])), Request: r}
						if target == final {
							if r.Header.Get("If-Match") != `"stable"` {
								t.Error("entity condition lost")
							}
							if channel == 0 {
								finalFirst.Add(1)
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
							}
						}
						return resp, nil
					}))
				}
				return transports
			}
			if _, err := s.Prefetch(context.Background(), original); err != nil {
				t.Fatal(err)
			}
			// Each later block must start from A independently; only that
			// block's retry resumes at B.
			if originalFirst.Load() != 3 || originalBackup.Load() != 0 || finalFirst.Load() != 2 || finalBackup.Load() != 2 {
				t.Fatalf("A first=%d A backup=%d B first=%d B backup=%d", originalFirst.Load(), originalBackup.Load(), finalFirst.Load(), finalBackup.Load())
			}
		})
	}
}
