package console

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"still-wanna-dance/internal/cacheproxy"
	"strings"
	"sync/atomic"
	"testing"
)

func consoleStorageError(t *testing.T, c *Console) string {
	t.Helper()
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status struct {
		StorageError string `json:"storageError"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	return status.StorageError
}

func TestLocalStorageFailureStopsConsoleFallback(t *testing.T) {
	for _, mode := range []string{"observation", "write", "index"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			const body = "local storage fixture"
			var queries, downloads atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, body) }))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				queries.Add(1)
				w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			cfg.OriginScheme = "http"
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			c.service = engine
			if mode == "write" {
				dir := filepath.Join(cfg.VideoDir, ".swd-tmp")
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sql.Open("sqlite", filepath.Join(filepath.Dir(cfg.VideoDir), "storage.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				table := "song_urls"
				if mode == "index" {
					table = "media"
				}
				if _, err := db.Exec("CREATE TRIGGER fail_storage BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'disk failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []int64{42, 43, 44} {
				if _, err := c.prefetchSong(context.Background(), engine, id); !errors.Is(err, cacheproxy.ErrLocalStorage) {
					t.Fatal(err)
				}
			}
			if _, err := c.resolvePlayback(context.Background(), "42"); !errors.Is(err, cacheproxy.ErrLocalStorage) {
				t.Fatal(err)
			}
			if queries.Load() != 1 {
				t.Fatal("retried API/node/song after local failure", queries.Load())
			}
			want := int32(1)
			// URL evidence and media metadata now commit during address resolution.
			if mode == "observation" || mode == "index" {
				want = 0
			}
			if downloads.Load() != want {
				t.Fatal("unexpected downloads", downloads.Load())
			}
			if got := consoleStorageError(t, c); got == "" || got != engine.StorageError().Error() {
				t.Fatalf("status lost the retained storage cause: %q", got)
			}
		})
	}
}

func TestStorageFailureStatusSurvivesRecoveryUntilRestart(t *testing.T) {
	for _, mode := range []string{"create", "publish"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			if got := consoleStorageError(t, c); got != "" {
				t.Fatal("uninitialized engine reported a fault", got)
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			const warm, cold = "existing cached video", "new uncached video"
			target := func(body string) string {
				return fmt.Sprintf("http://play.udon.dance/files/1/42-video.mp4?e=%x&s=%d", md5.Sum([]byte(body)), len(body))
			}
			var inject atomic.Bool
			var downloads atomic.Int32
			collision := filepath.Join(testStorageRoot(c.settings), "videos", fmt.Sprintf("%x.mp4", md5.Sum([]byte(cold))))
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				body := warm
				if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(cold))) {
					body = cold
					if mode == "publish" && inject.Swap(false) {
						if err := os.Mkdir(collision, 0700); err != nil {
							t.Error(err)
						}
					}
				}
				fmt.Fprint(w, body)
			}))
			defer origin.Close()
			cfg := fixtureCacheConfig()
			cfg.OriginScheme = "http"
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.service = engine
			if _, err := engine.Prefetch(context.Background(), target(warm)); err != nil {
				t.Fatal(err)
			}
			faultPath := collision
			if mode == "create" {
				faultPath = filepath.Join(cfg.VideoDir, ".swd-tmp")
				if err := os.Remove(faultPath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(faultPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			inject.Store(true)
			if _, err := engine.Prefetch(context.Background(), target(cold)); !errors.Is(err, cacheproxy.ErrLocalStorage) {
				t.Fatal("fault did not latch", err)
			}
			first := consoleStorageError(t, c)
			if first == "" || first != engine.StorageError().Error() {
				t.Fatal("status lost the first failure", first)
			}
			if err := os.Remove(faultPath); err != nil {
				t.Fatal(err)
			}
			if mode == "create" {
				if err := os.Mkdir(faultPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := downloads.Load()
			if _, err := engine.Prefetch(context.Background(), target(cold)); !errors.Is(err, cacheproxy.ErrLocalStorage) {
				t.Fatal("repair cleared the engine fault", err)
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest("GET", target(warm), nil))
			if w.Code != 200 || w.Body.String() != warm || w.Header().Get("X-SWD-Cache") != "HIT" || downloads.Load() != before {
				t.Fatal("cached read failed or cold download retried", w.Code, w.Body.String(), downloads.Load())
			}
			for range 2 {
				if err := c.start(); err != nil {
					t.Fatal(err)
				}
				if err := c.stop(); err != nil {
					t.Fatal(err)
				}
				if got := consoleStorageError(t, c); got != first {
					t.Fatalf("gateway actions cleared/replaced cause: %q", got)
				}
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			cfg.Store, cfg.Work = restarted.store, restarted.work
			restarted.service, err = cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := consoleStorageError(t, restarted); got != "" {
				t.Fatal("new engine retained an old fault", got)
			}
			if _, err := restarted.service.Prefetch(context.Background(), target(cold)); err != nil {
				t.Fatal("cold download failed after restart", err)
			}
		})
	}
}
