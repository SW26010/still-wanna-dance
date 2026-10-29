package cacheproxy

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResourceProtocolFallbackIncludesLaterRangeBodies(t *testing.T) {
	for _, mode := range []string{"headers", "short", "timeout", "invalid", "oversized", "certificate", "certificate-at-deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := setup(t, nil, func(c *Config) {
				c.OriginScheme = "https"
				c.BlockSize = 16
				c.DownloadTimeout = 100 * time.Millisecond
			})
			var mu sync.Mutex
			calls := make(map[string]int)
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					var start, end int64
					fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					mu.Lock()
					calls[fmt.Sprintf("%s/%d", r.URL.Scheme, start)]++
					mu.Unlock()
					resp := &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload))}}, ContentLength: end - start + 1, Body: io.NopCloser(strings.NewReader(payload[start : end+1])), Request: r}
					if r.URL.Scheme == "https" && start >= 16 {
						switch mode {
						case "headers":
							return nil, io.EOF
						case "short":
							resp.Body = io.NopCloser(strings.NewReader(payload[start:end]))
						case "timeout":
							resp.Body = stalledResourceBody{r.Context()}
						case "invalid":
							resp.Header.Set("Content-Range", "bytes 0-0/1")
						case "oversized":
							resp.Body = io.NopCloser(strings.NewReader(payload[start:end+1] + "x"))
						case "certificate":
							return nil, x509.UnknownAuthorityError{}
						case "certificate-at-deadline":
							<-r.Context().Done()
							return nil, x509.UnknownAuthorityError{}
						}
					}
					return resp, nil
				})}
			}
			_, err := s.Prefetch(context.Background(), videoURL(payload))
			fallback := mode == "headers" || mode == "short" || mode == "timeout"
			mu.Lock()
			defer mu.Unlock()
			if fallback {
				if err != nil || calls["http/16"] == 0 {
					t.Fatalf("calls=%v err=%v", calls, err)
				}
			} else {
				if err == nil {
					t.Fatal("accepted invalid resource")
				}
				for path := range calls {
					if strings.HasPrefix(path, "http/") {
						t.Fatalf("invalid downgrade: %v", calls)
					}
				}
			}
			if calls["http/0"] != 0 {
				t.Fatal("healthy HTTPS block used HTTP", calls)
			}
		})
	}
}

func TestDiscardProtocolRedirectAndFallback(t *testing.T) {
	var calls []string
	loader, err := NewDiscardLoader(DiscardConfig{Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		if r.URL.Query().Get("next") == "" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {videoURL(payload) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-35/%d", len(payload))}}, ContentLength: int64(len(payload)), Body: io.NopCloser(bytes.NewBufferString(payload)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer loader.Close()
	if err := loader.Load(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err, calls)
	}
	if strings.Join(calls, ",") != "https/,http/,https/1,http/1" {
		t.Fatal(calls)
	}
}

func TestResourceInitialBodyProtocolFallback(t *testing.T) {
	s, _ := setup(t, nil, func(c *Config) { c.OriginScheme = "https" })
	var calls []string
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Scheme)
			body := payload
			if r.URL.Scheme == "https" {
				body = body[:len(body)-1]
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-35/36"}}, ContentLength: 36, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err, calls)
	}
	if strings.Join(calls, ",") != "https,http" {
		t.Fatal(calls)
	}
}

func TestResourceRedirectFailureDoesNotDowngradeOrigin(t *testing.T) {
	s, _ := setup(t, nil, func(c *Config) {
		c.OriginScheme = "https"
		c.BlockSize = 16
	})
	var mu sync.Mutex
	var downgradedOrigin bool
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			if r.URL.Query().Get("next") != "" {
				return nil, io.EOF
			}
			if r.URL.Scheme == "http" {
				mu.Lock()
				downgradedOrigin = true
				mu.Unlock()
			}
			if start > 0 {
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {videoURL(payload) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload))}}, ContentLength: end - start + 1, Body: io.NopCloser(strings.NewReader(payload[start : end+1])), Request: r}, nil
		})}
	}
	if _, err := s.Prefetch(context.Background(), videoURL(payload)); err == nil {
		t.Fatal("accepted failed redirect target")
	}
	mu.Lock()
	defer mu.Unlock()
	if downgradedOrigin {
		t.Fatal("redirect target's failure downgraded the healthy origin")
	}
}
