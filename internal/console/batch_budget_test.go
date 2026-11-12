package console

import (
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/workfiles"
)

func TestBatchStopsAtBudget(t *testing.T) {
	for _, tc := range []struct {
		unlimited  bool
		limit      int64
		failRecord bool
	}{{true, 0, false}, {false, 0, false}, {false, 5, false}, {false, 20, false}, {false, 0, true}} {
		limit := tc.limit
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			c := testConsole(t)
			center := faults.New()
			c.SetFaultCenter(center)
			var previous error
			if !tc.unlimited && limit < 10 && !tc.failRecord {
				previous = errors.New("previous batch failed")
				center.Set("task:batch", previous)
				center.Take()
			}
			if tc.failRecord {
				path := c.files().State("batch-last-attempt")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), workfiles.TempName(path)), []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			defer c.Close()
			var downloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				serveTestVideo(w, r, strings.Repeat("x", 10))
			}))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/catalog" {
					writeTestMD5Catalog(w, map[int]string{1: strings.Repeat("x", 10), 2: strings.Repeat("x", 10), 3: strings.Repeat("x", 10), 4: strings.Repeat("x", 10)})
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
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.VideoBudgetBytes = limit
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			cfg.ResolvePlayback = c.resolvePlayback
			cfg.CacheUnlimited = tc.unlimited
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
			if !tc.unlimited && limit < 10 {
				want = 0
			}
			if downloads.Load() != want || c.batch.Failed != 0 || c.batch.BudgetReached != (!tc.unlimited && limit < 10) || c.batch.Running {
				t.Fatalf("downloads=%d batch=%+v", downloads.Load(), c.batch)
			}
			if !tc.unlimited && limit < 10 && (!strings.Contains(c.batch.Phase, "调整视频保留空间") || !c.lastBatch.Updated.IsZero()) {
				t.Fatalf("wrong stop result: %+v", c.batch)
			}
			if open, cause := center.Take(); open != tc.failRecord {
				t.Fatal("budget result and snapshot failure were not distinguished", cause)
			}
			if previous != nil {
				center.Restore(previous)
				if open, _ := center.Take(); !open {
					t.Fatal("budget stop incorrectly recovered an earlier batch fault")
				}
			}
		})
	}
}
