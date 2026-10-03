package console

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestBatchStopsAtBudget(t *testing.T) {
	for _, limit := range []int64{0, 5, 20} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			var downloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				fmt.Fprint(w, strings.Repeat("x", 10))
			}))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Api/Songs/list" {
					fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2},{"id":3},{"id":4}]}]}}`)
					return
				}
				host := "play.udon.dance"
				if r.URL.Query().Get("node") == "nya" {
					host = "nya.xin.moe"
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/%s-abc.mp4?e=%x&s=10", host, r.URL.Query().Get("id"), md5.Sum([]byte(strings.Repeat("x", 10)))))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.StorageDir, cfg.OriginScheme, cfg.MaxCacheBytes = c.settings.StorageDir, "http", limit
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			if err := c.startBatch(); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			done := c.batchDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("batch stalled")
			}
			want := int32(4)
			if limit > 0 {
				want = int32(limit / 10)
			}
			if downloads.Load() != want || c.batch.Failed != 0 || c.batch.BudgetReached != (limit > 0) || c.batch.Running {
				t.Fatalf("downloads=%d batch=%+v", downloads.Load(), c.batch)
			}
			if limit > 0 && (!strings.Contains(c.batch.Phase, "设置") || !c.lastBatch.Updated.IsZero()) {
				t.Fatalf("wrong stop result: %+v", c.batch)
			}
		})
	}
}

func TestBatchBudgetStopPreservesInFlightFallback(t *testing.T) {
	for _, mode := range []string{"same-resource", "fallback-fails", "larger-version"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			started, release, secondResolved := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var hkgDownloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "play.udon.dance" {
					close(started)
					<-release
					http.Error(w, "CF failed", http.StatusServiceUnavailable)
					return
				}
				hkgDownloads.Add(1)
				if mode == "fallback-fails" {
					http.Error(w, "HKG failed", http.StatusBadGateway)
					return
				}
				fmt.Fprint(w, strings.Repeat("x", 10))
			}))
			defer origin.Close()
			defer unblock()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Api/Songs/list" {
					fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":1},{"id":2}]}]}}`)
					return
				}
				id := r.URL.Query().Get("id")
				size, host := 10, "play.udon.dance"
				if id == "2" {
					select {
					case <-started:
					case <-r.Context().Done():
						return
					}
					size = 30
					defer close(secondResolved)
				}
				if r.URL.Query().Get("node") == "nya" {
					host = "nya.xin.moe"
					if mode == "larger-version" {
						size = 20
					}
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/%s-abc.mp4?e=%x&s=%d", host, id, md5.Sum([]byte(strings.Repeat("x", size))), size))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.StorageDir, cfg.OriginScheme, cfg.MaxCacheBytes = c.settings.StorageDir, "http", 10
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			if err := c.startBatch(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-secondResolved:
			case <-time.After(5 * time.Second):
				t.Fatal("second song not resolved")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				c.mu.Lock()
				current := c.batch.Current
				c.mu.Unlock()
				if !strings.Contains(current, "2 ·") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("budget rejection did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			unblock()
			c.mu.Lock()
			done := c.batchDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("fallback stalled")
			}
			if !c.batch.BudgetReached {
				t.Fatal("missing budget stop")
			}
			if mode == "same-resource" {
				if c.batch.Downloaded != 1 || c.batch.Failed != 0 || hkgDownloads.Load() != 1 {
					t.Fatalf("batch=%+v hkg=%d", c.batch, hkgDownloads.Load())
				}
			} else {
				if c.batch.Failed != 1 || c.batch.Checked != 1 || len(c.batch.Failures) != 1 || !strings.Contains(c.batch.Failures[0].Error, "503") {
					t.Fatalf("lost original failure: %+v", c.batch)
				}
				if mode == "larger-version" && (hkgDownloads.Load() != 0 || !strings.Contains(c.batch.Failures[0].Error, "回退受容量预算限制")) {
					t.Fatalf("batch=%+v hkg=%d", c.batch, hkgDownloads.Load())
				}
				if mode == "fallback-fails" && !strings.Contains(c.batch.Failures[0].Error, "502") {
					t.Fatalf("lost HKG failure: %+v", c.batch)
				}
			}
		})
	}
}
