package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestResourceCandidatesAlwaysUseHTTP(t *testing.T) {
	for _, sourceScheme := range []string{"http", "https"} {
		t.Run(sourceScheme, func(t *testing.T) {
			s, _ := setup(t, nil, func(c *Config) { c.BlockSize = 16 })
			var mu sync.Mutex
			var calls []string
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				var paths []http.RoundTripper
				for index := range 2 {
					paths = append(paths, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						mu.Lock()
						calls = append(calls, r.URL.Scheme)
						mu.Unlock()
						if index == 0 {
							return nil, io.EOF
						}
						var start, end int64
						fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
						return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload))}}, ContentLength: end - start + 1, Body: io.NopCloser(strings.NewReader(payload[start : end+1])), Request: r}, nil
					}))
				}
				return paths
			}
			target := strings.Replace(videoURL(payload), "http:", sourceScheme+":", 1)
			if _, err := s.Prefetch(t.Context(), target); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) < 4 {
				t.Fatal("did not exercise later ranges and candidates", calls)
			}
			for _, scheme := range calls {
				if scheme != "http" {
					t.Fatal(calls)
				}
			}
		})
	}
}

func TestDiscardUsesHTTPThroughValidatedRedirect(t *testing.T) {
	var calls []string
	loader, err := NewDiscardLoader(DiscardConfig{Transport: resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
		if r.URL.Query().Get("next") == "" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Replace(videoURL(payload), "http:", "https:", 1) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 0-35/36"}}, ContentLength: int64(len(payload)), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer loader.Close()
	if err := loader.Load(context.Background(), videoURL(payload)); err != nil {
		t.Fatal(err, calls)
	}
	if strings.Join(calls, ",") != "http/,http/1" {
		t.Fatal(calls)
	}
}

func TestResourceRejectsHTTPSUpgradeWithoutLoop(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, _ := setup(t, nil)
			var calls []string
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls = append(calls, r.URL.Scheme)
					return &http.Response{StatusCode: status, Header: http.Header{"Location": {strings.Replace(videoURL(payload), "http:", "https:", 1)}}, Body: http.NoBody, Request: r}, nil
				})}
			}
			if _, err := s.Prefetch(t.Context(), videoURL(payload)); err == nil || !strings.Contains(err.Error(), "revisits HTTP resource") {
				t.Fatal(err)
			}
			if strings.Join(calls, ",") != "http" {
				t.Fatal(calls)
			}
		})
	}
}
