package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"stepstash/internal/cacheproxy"
	"stepstash/internal/vrclog"
)

func TestBackgroundModesDownloadTwoSongsConcurrently(t *testing.T) {
	for _, mode := range []string{"batch", "queue"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			body := "concurrent video"
			started := make(chan string, 8)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- r.URL.Path
				select {
				case <-release:
					io.WriteString(w, body)
				case <-r.Context().Done():
				}
			}))
			defer origin.Close()
			defer unblock()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Api/Songs/list" {
					io.WriteString(w, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2},{"id":3}]}]}}`)
					return
				}
				w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/%s-abc.mp4?e=%x&s=%d", r.URL.Query().Get("id"), md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.OriginScheme = "http"
			cfg.StorageDir = c.settings.StorageDir
			cfg.Origins["nya.xin.moe"] = strings.TrimPrefix(origin.URL, "http://")
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var done <-chan struct{}
			if mode == "batch" {
				if err := c.startBatch(); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				done = c.batchDone
				c.mu.Unlock()
			} else {
				c.queue = QueueStatus{Running: true, Songs: []vrclog.Song{{ID: 1}, {ID: 2}, {ID: 3}}}
				queueDone := make(chan struct{})
				done = queueDone
				go func() { defer close(queueDone); c.queueWorker(ctx, engine, make(chan struct{})) }()
				defer func() { cancel(); <-queueDone }()
			}
			seen := map[string]bool{}
			for i := 0; i < 2; i++ {
				select {
				case path := <-started:
					seen[path] = true
				case <-time.After(3 * time.Second):
					t.Fatal("second download did not start concurrently")
				}
			}
			if len(seen) != 2 {
				t.Fatal("duplicate download", seen)
			}
			select {
			case path := <-started:
				t.Fatal("third download started before capacity freed", path)
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			deadline := time.After(5 * time.Second)
			for {
				c.mu.Lock()
				complete := c.batch.Checked == 3 && c.batch.Failed == 0
				if mode == "queue" {
					complete = c.queue.Completed == 3
				}
				c.mu.Unlock()
				if complete {
					break
				}
				select {
				case <-deadline:
					t.Fatal("downloads did not complete")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if mode == "batch" {
				<-done
			}
		})
	}
}
