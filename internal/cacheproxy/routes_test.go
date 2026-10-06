package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type resourceTransportFunc func(*http.Request) (*http.Response, error)

func (f resourceTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResourceDownloadUsesMeasuredChannelsWithoutChangingURL(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("unmeasured transport used") })
	target := videoURL(payload)
	var called []int
	s.cfg.ResourceTransports = func(got string) []http.RoundTripper {
		if got != target {
			t.Errorf("address changed: %s", got)
		}
		var transports []http.RoundTripper
		for i := 0; i < 2; i++ {
			transports = append(transports, resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				called = append(called, i)
				if i == 0 {
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
	if len(called) != 2 || called[0] != 0 || called[1] != 1 {
		t.Fatal(called)
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
	if calls != 4 {
		t.Fatal("unbounded fallback", calls)
	}
}
