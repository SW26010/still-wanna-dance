package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
)

func TestVideoRequestActivityHasNoSecondQuota(t *testing.T) {
	b := new(requestActivity)
	var releases []func()
	for range videoRequestSoftLimit + 3 {
		release, err := b.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.active != len(releases) {
		t.Fatal("incorrect active count", b.active)
	}
	for _, release := range releases {
		release()
		release()
	}
	if b.active != 0 {
		t.Fatal("request activity leaked")
	}
}

type delayedRequestClose struct {
	started, finish chan struct{}
	once            sync.Once
}

func (b *delayedRequestClose) Read([]byte) (int, error) { return 0, io.EOF }
func (b *delayedRequestClose) Close() error {
	b.once.Do(func() { close(b.started); <-b.finish })
	return nil
}

func TestVideoRequestActivityHoldsResponseUntilCloseCompletes(t *testing.T) {
	s, _ := setup(t, nil)
	s.requests = new(requestActivity)
	body := &delayedRequestClose{started: make(chan struct{}), finish: make(chan struct{})}
	defer close(body.finish)
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
		})}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", videoURL(payload), nil)
	resp, err := s.resourceResponse(r, new(resourceAttempts))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	closed := make(chan struct{})
	go func() { resp.Body.Close(); close(closed) }()
	<-body.started
	next, err := s.requests.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next()
	s.requests.mu.Lock()
	active := s.requests.active
	s.requests.mu.Unlock()
	if active != 1 {
		t.Fatalf("canceled response stopped counting during Close: %d", active)
	}
	// Complete Close before checking for the final, exactly-once release.
	body.finish <- struct{}{}
	<-closed
	resp.Body.Close()
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	if s.requests.active != 0 {
		t.Fatal("request activity leaked")
	}
}

func TestVideoRequestActivityReleasesFailedAttempts(t *testing.T) {
	for _, status := range []int{0, 503} {
		s, _ := setup(t, nil)
		s.cfg.ResourceTransports = func(string) []http.RoundTripper {
			return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
				if status == 0 {
					return nil, errors.New("controlled transport error")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody, Request: r}, nil
			})}
		}
		r, _ := http.NewRequest("GET", videoURL(payload), nil)
		if _, err := s.resourceResponse(r, new(resourceAttempts)); err == nil {
			t.Fatal("failure accepted")
		}
		s.requests.mu.Lock()
		active := s.requests.active
		s.requests.mu.Unlock()
		if active != 0 {
			t.Fatalf("failed attempt leaked %d slots", active)
		}
	}
}
