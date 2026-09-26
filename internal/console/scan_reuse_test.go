package console

import (
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

	"stepstash/internal/cacheproxy"
)

func TestDownloadReusesScan(t *testing.T) {
	for _, mode := range []string{"hit", "missing", "removed", "changed", "stale", "cf", "failed", "settings"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			defer c.Close()
			if mode == "cf" {
				c.settings.DownloadUpstream = "cf"
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			const body = "scan reuse fixture"
			path := fixtureVideoPath(c.settings.StorageDir, "138", body)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if mode != "missing" && mode != "stale" && mode != "failed" {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var lists, resolves, downloads atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Api/Songs/list" {
					lists.Add(1)
					fmt.Fprint(w, `{"groups":{"contents":[{"songInfos":[{"id":138}]}]}}`)
					return
				}
				n := resolves.Add(1)
				if mode == "failed" && n == 1 {
					http.Error(w, "failure", 503)
					return
				}
				host := "nya.xin.moe"
				if r.URL.Query().Get("node") == "cf" {
					host = "play.udon.dance"
				}
				extra := ""
				if mode == "stale" && n == 1 {
					extra = "&old=1"
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/138-abc.mp4?e=%x&s=%d%s", host, md5.Sum([]byte(body)), len(body), extra))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			run := func(scan bool) {
				t.Helper()
				if err := c.startBatchMode(scan); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				done := c.batchDone
				c.mu.Unlock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("batch stalled")
				}
			}
			run(true)
			if c.scanPlan == nil {
				t.Fatal("no scan plan")
			}
			if mode == "removed" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed" {
				if err := os.WriteFile(path, []byte(strings.Repeat("x", len(body))), 0600); err != nil {
					t.Fatal(err)
				}
				stamp := time.Now().Add(time.Hour)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "settings" {
				if err := c.save(c.settings); err != nil {
					t.Fatal(err)
				}
			}
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				if r.URL.Query().Get("old") == "1" {
					http.Error(w, "expired", 403)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer origin.Close()
			cfg := cacheproxy.DefaultConfig()
			cfg.OriginScheme = "http"
			cfg.StorageDir = c.settings.StorageDir
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			run(false)
			if c.batch.Failed != 0 || c.batch.Checked != 1 {
				t.Fatalf("batch: %+v", c.batch)
			}
			wantLists, wantResolves := int32(1), int32(1)
			if mode == "cf" || mode == "stale" || mode == "failed" || mode == "settings" {
				wantResolves = 2
			}
			if mode == "settings" {
				wantLists = 2
			}
			if lists.Load() != wantLists || resolves.Load() != wantResolves {
				t.Fatalf("lists=%d resolves=%d", lists.Load(), resolves.Load())
			}
			if mode == "hit" && (downloads.Load() != 0 || c.batch.Hits != 1) {
				t.Fatal("hit downloaded again")
			}
			if mode == "missing" || mode == "removed" || mode == "changed" || mode == "stale" || mode == "failed" {
				if c.batch.Downloaded != 1 {
					t.Fatalf("not repaired: %+v", c.batch)
				}
			}
			if c.scanPlan != nil {
				t.Fatal("plan should be consumed")
			}
		})
	}
}

func TestCanceledScanDiscardsReusablePlan(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c.scanPlan = &scanPlan{settings: c.settings}
	started := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	done, cancel := c.batchDone, c.batchCancel
	c.mu.Unlock()
	defer cancel()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("scan did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scan did not stop")
	}
	if c.scanPlan != nil {
		t.Fatal("canceled scan retained a reusable plan")
	}
}
