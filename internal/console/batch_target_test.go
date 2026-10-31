package console

import (
	"context"
	"crypto/md5"
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
)

func TestTargetDownloadUsesSharedPublicationAndBudget(t *testing.T) {
	for _, mode := range []string{"song", "resource", "unresolved", "blocked", "budget", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			const body = "shared complete target"
			key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
			var lists, downloads, resolves atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, body) }))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/catalog" {
					lists.Add(1)
					http.Error(w, "must not refresh", 500)
					return
				}
				resolves.Add(1)
				w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/%s-abc.mp4?e=%s&s=%d", r.URL.Query().Get("id"), key, len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.checksumURL, c.client.Transport = api.URL, api.URL+"/catalog", http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.CacheUnlimited = mode != "budget"
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			cfg.VideoBudgetBytes = 0
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			ctx := context.Background()
			if mode == "unresolved" {
				if err := engine.SetSongPolicy(ctx, 1, cacheproxy.SongFavorite); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := engine.SyncCatalog(ctx, cacheproxy.Catalog{Revision: "20261024000000", Source: "test", Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: key}, {ID: 2, MD5: key}}}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "blocked" {
				if err := engine.SetSongPolicy(ctx, 1, cacheproxy.SongBlocked); err != nil {
					t.Fatal(err)
				}
			}
			target := cacheproxy.DownloadTarget{SongID: "1"}
			if mode == "resource" {
				target = cacheproxy.DownloadTarget{Resource: key}
			}
			if mode == "orphan" {
				target = cacheproxy.DownloadTarget{Resource: strings.Repeat("f", 32)}
			}
			// Existing queue lifecycle intent/cancel handle must remain untouched.
			c.queueDesired = true
			c.queue.Running = true
			c.queueCancel = func() { t.Error("manual target canceled queue") }
			defer func() { c.queueCancel = nil; c.queue.Running = false }()
			c.lastBatch = Batch{Total: 99, Phase: "previous full result"}
			if err := c.startBatchTarget(cacheproxy.CacheStorageID(fixtureLibraryPaths(filepath.Dir(cfg.VideoDir))), target); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			done := c.batchDone
			c.mu.Unlock()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("target download stalled")
			}
			if lists.Load() != 0 || !c.queueDesired || !c.queue.Running || c.lastBatch.Total != 99 {
				t.Fatal(lists.Load(), c.queue, c.lastBatch)
			}
			success := mode == "song" || mode == "resource" || mode == "unresolved"
			if success {
				data, err := os.ReadFile(filepath.Join(cfg.VideoDir, key+".mp4"))
				if err != nil || string(data) != body || downloads.Load() != 1 || c.batch.Failed != 0 {
					t.Fatal(string(data), err, downloads.Load(), c.batch)
				}
				want := 1
				if mode == "resource" {
					want = 2
				}
				if c.batch.Downloaded != want || c.batch.Total != want {
					t.Fatal(c.batch)
				}
			} else if downloads.Load() != 0 {
				t.Fatal("ineligible target downloaded", c.batch)
			}
			if mode == "budget" && !c.batch.BudgetReached {
				t.Fatal(c.batch)
			}
			if mode == "blocked" && (resolves.Load() != 0 || c.batch.Skipped != 1) {
				t.Fatal(resolves.Load(), c.batch)
			}
			if mode == "orphan" && (resolves.Load() != 0 || !strings.Contains(c.batch.Phase, "无当前歌曲关联")) {
				t.Fatal(resolves.Load(), c.batch)
			}
		})
	}
}

func TestTargetDownloadAPIRejectsInvalidRequestsBeforeStarting(t *testing.T) {
	c := testConsole(t)
	for _, body := range []string{`{}`, `{"songID":"01"}`, `{"songID":"1","resource":"x"}`, `{"resource":"../x"}`, `{"songID":"1","storageID":"stale"}`, `{"songID":"1","unknown":true}`} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/batch/download", strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.batch.Running {
		t.Fatal("invalid target started engine")
	}
}
