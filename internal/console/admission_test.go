package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			c.settings.QueuePrefetchCount = 4
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
