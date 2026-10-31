package console

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Exercise the real HTTP resolver through the cache server, rather than a
// ResolvePlayback stub that would hide cancellation.
func TestPlaybackSlowCFIntegration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached bool
		delay  time.Duration
	}{
		{"cold CF slow", false, 3500 * time.Millisecond},
		{"cached background refresh", true, 6500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testConsole(t)
			const old, fresh = "old cached video", "new slow upstream video"
			target := func(host, body string) string {
				return fmt.Sprintf("http://%s/files/2403/42-abc.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body))
			}
			var offline atomic.Bool
			var nodes sync.Map
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if offline.Load() {
					http.Error(w, "offline", http.StatusServiceUnavailable)
					return
				}
				node := r.URL.Query().Get("node")
				nodes.Store(node, true)
				timer := time.NewTimer(tc.delay)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
					return
				case <-timer.C:
				}
				host := "nya.xin.moe"
				if node == "cf" {
					host = "play.udon.dance"
				}
				w.Header().Set("Location", target(host, fresh))
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := fresh
				if r.URL.Query().Get("e") == fmt.Sprintf("%x", md5.Sum([]byte(old))) {
					body = old
				}
				io.WriteString(w, body)
			}))
			defer origin.Close()
			cfg := fixtureCacheConfig()
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			cfg.ResolvePlayback = c.resolvePlayback
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if tc.cached {
				if _, err := engine.PrefetchSong(context.Background(), "42", target("nya.xin.moe", old)); err != nil {
					t.Fatal(err)
				}
			}
			request := func() *httptest.ResponseRecorder {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				w := httptest.NewRecorder()
				engine.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42&node=nya", nil).WithContext(ctx))
				return w
			}
			started := time.Now()
			w := request()
			want := fresh
			if tc.cached {
				want = old
			}
			if w.Code != http.StatusOK || w.Body.String() != want {
				t.Fatalf("playback: %d %q; want %q", w.Code, w.Body.String(), want)
			}
			if tc.cached {
				if elapsed := time.Since(started); elapsed > time.Second {
					t.Fatalf("local hit waited %v", elapsed)
				}
				time.Sleep(tc.delay + 100*time.Millisecond)
				offline.Store(true)
				w = request()
				if w.Code != 200 || w.Body.String() != old {
					t.Fatalf("local content replaced: %d %q", w.Code, w.Body.String())
				}
			}
			wantNodes := []string{"cf"}
			if _, ok := nodes.Load("nya"); ok {
				t.Fatal("queried HKG")
			}
			for _, node := range wantNodes {
				if _, ok := nodes.Load(node); !ok {
					t.Errorf("route %s was not queried", node)
				}
			}
		})
	}
}

func TestPlaybackResolutionInvalidVideoDoesNotFallback(t *testing.T) {
	const valid = "/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1"
	for _, invalid := range []string{
		"/video.mp4?e=00000000000000000000000000000000&s=1",
		"/files/1/%32-video.mp4?e=00000000000000000000000000000000&s=1",
		"/files/1/2-video.mp4?e=invalid&s=1",
		"/files/1/2-video.mp4?s=1",
		valid + "&e=00000000000000000000000000000000",
		strings.Replace(valid, "s=1", "s=invalid", 1),
		strings.Replace(valid, "s=1", "s=0", 1),
		strings.Replace(valid, "s=1", "s=-1", 1),
		strings.Replace(valid, "s=1", "s=2147483649", 1),
		valid + "&s=1",
		valid + "&token=%zz",
	} {
		t.Run(invalid, func(t *testing.T) {
			c := testConsole(t)
			var calls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Query().Get("node") != "cf" {
					t.Errorf("unexpected node: %s", r.URL)
				}
				w.Header().Set("Location", "https://play.udon.dance"+invalid)
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			target, err := c.resolvePlayback(context.Background(), "42")
			if err == nil || target != "" || calls.Load() != 1 {
				t.Fatalf("target=%q err=%v calls=%d", target, err, calls.Load())
			}
		})
	}
}

func TestPlaybackResolutionOnlyUsesCF(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%v", failed), func(t *testing.T) {
			c := testConsole(t)
			const target = "https://nya.xin.moe/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1"
			var calls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				node := r.URL.Query().Get("node")
				if node != "cf" {
					t.Errorf("unexpected node=%s", node)
				}
				if failed && node == "cf" {
					http.Error(w, "CF failed", 503)
					return
				}
				w.Header().Set("Location", target)
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			got, err := c.resolvePlayback(context.Background(), "42")
			if (err != nil) != failed || (!failed && got != target) || calls.Load() != 1 {
				t.Fatalf("target=%q err=%v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestPlaybackResolutionCancellation(t *testing.T) {
	c := testConsole(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 1 {
			t.Error("unexpected retry after cancellation")
			return
		}
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.resolvePlayback(ctx, "42"); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream request survived cancellation")
	}
}
