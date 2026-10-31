package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestRemovedControlsDoNotStartWork(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.hosts.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/cache", "/api/start", "/api/stop", "/api/queue/start", "/api/queue/stop", "/api/hosts/enable", "/api/hosts/disable"} {
		method := http.MethodPost
		if path == "/api/cache" {
			method = http.MethodGet
		}
		r := httptest.NewRequest(method, "http://"+c.address+path, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if c.service != nil || c.httpServer != nil || c.queueDesired || c.queue.Running || c.hosts.session != nil {
		t.Fatal("removed controls started work")
	}
	if after, err := os.ReadFile(c.hosts.path); err != nil || string(after) != string(before) {
		t.Fatal("removed controls changed hosts", err)
	}
}

func TestConsolePrefetchPrefersCFEvenWhenHKGIsFaster(t *testing.T) {
	c := testConsole(t)
	const body = "shared route fixture"
	var selected atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			if r.Host == "play.udon.dance" {
				time.Sleep(60 * time.Millisecond)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)))
			w.WriteHeader(206)
		}
		selected.Store(r.Host)
		io.WriteString(w, body)
	}))
	defer origin.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := "nya.xin.moe"
		if r.URL.Query().Get("node") == "cf" {
			host = "play.udon.dance"
		}
		w.Header().Set("Location", fmt.Sprintf("http://%s/files/1/2-video.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.client.Transport = http.DefaultTransport
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, c.settings.VideoDir
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err = c.prefetchSong(context.Background(), engine, 42); err != nil {
		t.Fatal(err)
	}
	if selected.Load() != "play.udon.dance" {
		t.Fatalf("CF preference was overridden: %v", selected.Load())
	}
}
