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
)

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
	cfg.OriginScheme = "http"
	cfg.StorageDir = t.TempDir()
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err = c.prefetchSong(context.Background(), engine, 42, nil); err != nil {
		t.Fatal(err)
	}
	if selected.Load() != "play.udon.dance" {
		t.Fatalf("CF preference was overridden: %v", selected.Load())
	}
}
