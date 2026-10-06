package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
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
