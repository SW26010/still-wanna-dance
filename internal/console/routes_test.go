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

	"stepstash/internal/cacheproxy"
)

func TestResolveRoutesQueriesBothNodesConcurrently(t *testing.T) {
	c := testConsole(t)
	started := make(chan string, 2)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Query().Get("node")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		host := "nya.xin.moe"
		if r.URL.Query().Get("node") == "cf" {
			host = "play.udon.dance"
		}
		w.Header().Set("Location", "http://"+host+"/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1")
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.client.Transport = http.DefaultTransport
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		urls, err := c.resolveRoutes(ctx, "42", "auto")
		if err == nil && len(urls) != 2 {
			err = fmt.Errorf("got %d routes", len(urls))
		}
		done <- err
	}()
	nodes := map[string]bool{}
	for range 2 {
		select {
		case node := <-started:
			nodes[node] = true
		case <-ctx.Done():
			t.Fatal("route queries were not concurrent")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !nodes["cf"] || !nodes["nya"] {
		t.Fatal(nodes)
	}
}

func TestConsolePrefetchUsesUnifiedRouteSelection(t *testing.T) {
	c := testConsole(t)
	const body = "shared route fixture"
	var selected atomic.Value
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			if r.Host == "nya.xin.moe" {
				time.Sleep(60 * time.Millisecond)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)))
			w.WriteHeader(206)
		} else {
			selected.Store(r.Host)
		}
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
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = t.TempDir()
	cfg.ResolveRoutes = func(ctx context.Context, id string) ([]string, error) { return c.resolveRoutes(ctx, id, "auto") }
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
		t.Fatalf("slower initial HKG route was used: %v", selected.Load())
	}
}
