package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/vrclog"
)

func TestOrderedPreparationStopsResolutionAtUsageCapacity(t *testing.T) {
	for _, mode := range []string{"batch", "queue"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			resolved := make(chan string, 8)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/catalog" {
					writeTestMD5Catalog(w, map[int]string{100001: "100001", 100002: "100002", 100003: "100003", 100004: "100004"})
					return
				}
				id := r.URL.Query().Get("id")
				resolved <- id
				// Two 1024-block resources fit the 2867-block non-GET threshold.
				// The third must wait; no body or large fixture is materialized.
				w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/%s-abc.mp4?e=%x&s=%d", id, md5.Sum([]byte(id)), 1<<30))
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.StorageDir, cfg.OriginScheme = c.settings.StorageDir, "http"
			cfg.Origins["nya.xin.moe"] = strings.TrimPrefix(origin.URL, "http://")
			cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
				return c.resolvePlayback(ctx, id, node, "auto")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			c.service = engine
			wake := make(chan struct{}, 1)
			var done <-chan struct{}
			var cancel context.CancelFunc
			if mode == "batch" {
				if err := c.startBatch(); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				cancel, done = c.batchCancel, c.batchDone
				c.mu.Unlock()
			} else {
				var ctx context.Context
				ctx, cancel = context.WithCancel(context.Background())
				c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 100001}, {ID: 100002}, {ID: 100003}, {ID: 100004}}}
				queueDone := make(chan struct{})
				done = queueDone
				go func() { defer close(queueDone); c.queueWorker(ctx, engine, wake) }()
			}
			defer func() { cancel(); <-done }()
			take := func(want string) {
				t.Helper()
				select {
				case id := <-resolved:
					if id != want {
						t.Fatal("resolution lost source order", id, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("ordered source made no progress", want)
				}
			}
			for i := 1; i <= 3; i++ {
				id := i
				if mode == "batch" {
					id = 5 - i
				}
				take(fmt.Sprint(100000 + id))
			}
			select {
			case id := <-resolved:
				t.Fatal("resolved past the single pending source", id)
			case <-time.After(100 * time.Millisecond):
			}
			if mode == "queue" {
				// Remove an ordinary member: its usage ends immediately, while
				// the head remains valid. The pending third source admits and
				// the fourth is now resolved without any download completing.
				c.mu.Lock()
				c.setQueueSongsLocked([]vrclog.Song{{ID: 100001}, {ID: 100003}, {ID: 100004}}, false)
				c.mu.Unlock()
				wake <- struct{}{}
				take("100004")
			}
		})
	}
}

func TestQueueRepreparesRearmedMemberAfterPreviousSuccess(t *testing.T) {
	c := testConsole(t)
	const body = "rearmed queue fixture"
	target := fmt.Sprintf("https://play.udon.dance/files/2403/42-abc.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body))
	cfg := fixtureCacheConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.CacheUnlimited, cfg.MaxCacheBytes = false, 0
	cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return target, nil }
	var calls atomic.Int32
	cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{catalogTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusPartialContent, Request: r, ContentLength: int64(len(body)),
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body))}},
				Body:   io.NopCloser(strings.NewReader(body))}, nil
		})}
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	// After completed usage expires and a real clock change rearms the member,
	// the console still remembers success but the engine needs admission again.
	c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 42}}, prepared: map[int64]bool{42: true}}
	ctx, cancel := context.WithCancel(t.Context())
	done, wake := make(chan struct{}), make(chan struct{}, 1)
	go func() { defer close(done); c.queueWorker(ctx, engine, wake) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		ready := c.queue.Completed == 1 && c.queue.prepared[42]
		c.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("previous success blocked rearmed preparation", calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
	for range 3 {
		wake <- struct{}{}
		time.Sleep(10 * time.Millisecond)
	}
	if engine.QueueNeedsPreparation(42) || calls.Load() != 1 {
		t.Fatal("valid completed usage was prepared repeatedly", calls.Load())
	}
}
