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

func urgentRequestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) { return true, nil }))
}

func waitRequestQueue(t *testing.T, b *requestBudget, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		b.mu.Lock()
		n, changed := len(b.waiters), b.changed
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

func TestVideoRequestBudgetReservesCapacityAndPrioritizesReaders(t *testing.T) {
	b := newRequestBudget(2, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	background, err := b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer background()
	gotBackground := make(chan func(), 1)
	go func() { release, _ := b.acquire(ctx); gotBackground <- release }()
	waitRequestQueue(t, b, 1)
	reader, err := b.acquire(urgentRequestContext(ctx))
	if err != nil {
		t.Fatal("reader was blocked by speculative work", err)
	}
	defer reader()
	gotReader := make(chan func(), 1)
	go func() { release, _ := b.acquire(urgentRequestContext(ctx)); gotReader <- release }()
	waitRequestQueue(t, b, 2)
	background()
	select {
	case release := <-gotReader:
		if release == nil {
			t.Fatal("queued reader canceled")
		}
		defer release()
	case <-ctx.Done():
		t.Fatal("background waiter overtook reader")
	}
	select {
	case <-gotBackground:
		t.Fatal("exceeded total capacity")
	default:
	}
	reader()
	select {
	case release := <-gotBackground:
		if release == nil {
			t.Fatal("queued background canceled")
		}
		release()
	case <-ctx.Done():
		t.Fatal("background did not resume")
	}
}

func TestVideoRequestBudgetPromotesQueuedWorkAndRemovesCanceledWaiters(t *testing.T) {
	b := newRequestBudget(2, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	canceledCtx, stop := context.WithCancel(ctx)
	canceled := make(chan error, 1)
	go func() { _, err := b.acquire(canceledCtx); canceled <- err }()
	waitRequestQueue(t, b, 1)
	var mu sync.Mutex
	urgent, changed := false, make(chan struct{})
	promotedCtx := context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) {
		mu.Lock()
		defer mu.Unlock()
		return urgent, changed
	}))
	promoted := make(chan func(), 1)
	go func() { release, _ := b.acquire(promotedCtx); promoted <- release }()
	waitRequestQueue(t, b, 2)
	mu.Lock()
	urgent = true
	close(changed)
	changed = make(chan struct{})
	mu.Unlock()
	select {
	case release := <-promoted:
		if release == nil {
			t.Fatal("promotion failed")
		}
		release()
	case <-ctx.Done():
		t.Fatal("queued block did not use reader reserve")
	}
	stop()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitRequestQueue(t, b, 0)
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
	s.requests = newRequestBudget(1, 1)
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
	go func() { _, err := s.requests.acquire(urgentRequestContext(waitCtx)); done <- err }()
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
	if s.requests.active != 0 || s.requests.speculative != 0 {
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

func TestVideoRequestBudgetDemotionWakesEarlierWaiter(t *testing.T) {
	b := newRequestBudget(1, 1)
	// Arrange the interleaving where A observes B as urgent, then B demotes
	// before evaluating its own queue position. Capacity is already available;
	// no active HTTP completion remains to rescue a missed wakeup.
	b.active, b.speculative = 1, 1
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	wakeA, wakeB := make(chan struct{}), make(chan struct{})
	urgent, demoteOnObservation := true, false // accessed under b.mu
	ctxA := context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) { return false, wakeA }))
	ctxB := context.WithValue(ctx, requestPriorityKey{}, requestPriority(func() (bool, <-chan struct{}) {
		observed := urgent
		if demoteOnObservation && urgent {
			urgent = false
			close(wakeB)
			wakeB = make(chan struct{})
		}
		return observed, wakeB
	}))
	gotA, gotB := make(chan func(), 1), make(chan func(), 1)
	go func() { release, _ := b.acquire(ctxA); gotA <- release }()
	waitRequestQueue(t, b, 1)
	go func() { release, _ := b.acquire(ctxB); gotB <- release }()
	waitRequestQueue(t, b, 2)
	b.mu.Lock()
	b.active, b.speculative = 0, 0
	demoteOnObservation = true
	close(wakeA)
	wakeA = make(chan struct{})
	b.mu.Unlock()
	select {
	case release := <-gotA:
		if release == nil {
			t.Fatal("demotion left available capacity unused")
		}
		release()
	case <-ctx.Done():
		t.Fatal("earlier waiter was not notified of demotion")
	}
	select {
	case release := <-gotB:
		if release == nil {
			t.Fatal("demoted waiter did not follow FIFO")
		}
		release()
	case <-ctx.Done():
		t.Fatal("demoted waiter stalled")
	}
}
