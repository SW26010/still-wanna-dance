package console

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			want := int32(1)
			if limit > 0 && limit < 10 {
				want = 0
			}
			if downloads.Load() != want || c.batch.Failed != 0 || c.batch.BudgetReached != (limit > 0 && limit < 10) || c.batch.Running {
				t.Fatalf("downloads=%d batch=%+v", downloads.Load(), c.batch)
			}
			if limit > 0 && limit < 10 && (!strings.Contains(c.batch.Phase, "设置") || !c.lastBatch.Updated.IsZero()) {
				t.Fatalf("wrong stop result: %+v", c.batch)
			}
		})
	}
}
