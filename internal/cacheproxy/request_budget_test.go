package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func waitRequestQueue(t *testing.T, b *requestBudget, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		b.mu.Lock()
		n, changed := b.waiting, b.changed
		b.mu.Unlock()
		if n == count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %d queued requests, got %d", count, n)
		case <-changed:
		}
	}
}

func TestVideoRequestBudgetCapacityCancellationAndReuse(t *testing.T) {
	b := newRequestBudget(1)
	held, err := b.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.acquire(ctx); done <- err }()
	waitRequestQueue(t, b, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitRequestQueue(t, b, 0)
	held()
	held()
	next, err := b.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next()
	if b.active != 0 {
		t.Fatal("quota leaked")
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

func TestVideoRequestBudgetHoldsResponseUntilCloseCompletes(t *testing.T) {
	s, _ := setup(t, nil)
	s.requests = newRequestBudget(1)
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
	waitCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.requests.acquire(waitCtx); done <- err }()
	waitRequestQueue(t, s.requests, 1)
	s.requests.mu.Lock()
	active := s.requests.active
	s.requests.mu.Unlock()
	if active != 1 {
		t.Fatalf("canceled response released quota during Close: %d", active)
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Complete Close before checking for the final, exactly-once release.
	body.finish <- struct{}{}
	<-closed
	resp.Body.Close()
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	if s.requests.active != 0 {
		t.Fatal("quota leaked")
	}
}

func TestVideoRequestBudgetReleasesFailedAttempts(t *testing.T) {
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
