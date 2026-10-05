package console

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestQueuePrefetchSettings(t *testing.T) {
	c := testConsole(t)
	if c.settings.QueuePrefetchCount != 3 {
		t.Fatalf("legacy default: %d", c.settings.QueuePrefetchCount)
	}
	for _, count := range []int{1, 5, 100, 0} {
		s := c.settings
		s.QueuePrefetchCount = count
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		reopened, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		want := count
		if want == 0 {
			want = 3
		}
		got := reopened.settings.QueuePrefetchCount
		reopened.Close()
		if got != want {
			t.Fatalf("persisted count: got %d, want %d", got, want)
		}
	}
	for _, count := range []int{-1, 101} {
		s := c.settings
		s.QueuePrefetchCount = count
		if err := c.save(s); err == nil {
			t.Fatalf("accepted invalid count %d", count)
		}
	}
}

func TestQueueWorkerConfiguredWindow(t *testing.T) {
	for _, count := range []int{1, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c := testConsole(t)
			c.settings.QueuePrefetchCount = count
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.StorageDir = c.settings.StorageDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			// An unsupported entry occupies a position in the larger window.
			c.queue = QueueStatus{Songs: []vrclog.Song{{ID: 1}, {ID: -1}, {ID: 3}, {ID: 4}, {ID: 5}, {ID: 6}}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); c.queueWorker(ctx, engine, make(chan struct{})) }()
			defer func() { cancel(); <-done }()
			want := count
			if count > 1 {
				want--
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				c.mu.Lock()
				finished := len(c.queue.Failures) == want && len(c.queue.Active) == 0
				if finished {
					for _, f := range c.queue.Failures {
						if f.ID > int64(count) || f.ID <= 0 {
							t.Errorf("processed out-of-window song %d", f.ID)
						}
					}
				}
				c.mu.Unlock()
				if finished {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not process configured window")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestQueueConfiguredWindowInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := QueueStatus{prefetchCount: 1, prepared: map[int64]bool{1: true},
		waiters: map[int64]*queueWaiter{1: {ctx: ctx, cancel: cancel}}}
	q.setSongs([]vrclog.Song{{ID: 2}, {ID: 1}}, false)
	if ctx.Err() == nil || q.prepared[1] || q.wants(1, 0) || !q.wants(2, 0) {
		t.Fatal("moving outside configured window did not invalidate song")
	}
}
