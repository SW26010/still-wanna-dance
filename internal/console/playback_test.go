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
// ResolvePlayback stub that would hide per-route cancellation.
func TestPlaybackSlowAutoIntegration(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		cached, stalledPreferred bool
		delay                    time.Duration
	}{
		{"cold both slow", false, false, 3500 * time.Millisecond},
		{"cold alternate slow", false, true, 3500 * time.Millisecond},
		{"cached background refresh", true, false, 6500 * time.Millisecond},
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
				if tc.stalledPreferred && node == "nya" {
					<-r.Context().Done()
					return
				}
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
			cfg.StorageDir, cfg.OriginScheme = c.settings.StorageDir, "http"
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
				return c.resolvePlayback(ctx, id, node, "auto")
			}
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
				engine.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil).WithContext(ctx))
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
			for _, node := range []string{"nya", "cf"} {
				if _, ok := nodes.Load(node); !ok {
					t.Errorf("route %s was not queried", node)
				}
			}
		})
	}
}

func TestPlaybackResolutionInvalidVideoFallback(t *testing.T) {
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
		for _, preferred := range []string{"nya", "cf"} {
			for _, mode := range []string{"auto", "fixed", "both invalid"} {
				t.Run(preferred+"/"+mode+"/"+invalid, func(t *testing.T) {
					c := testConsole(t)
					var mu sync.Mutex
					var nodes []string
					api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						node := r.URL.Query().Get("node")
						mu.Lock()
						nodes = append(nodes, node)
						mu.Unlock()
						host := "nya.xin.moe"
						if node == "cf" {
							host = "play.udon.dance"
						}
						path := valid
						if node == preferred || mode == "both invalid" {
							path = invalid
						}
						w.Header().Set("Location", "https://"+host+path)
						w.WriteHeader(http.StatusFound)
					}))
					defer api.Close()
					c.apiBase = api.URL
					c.client.Transport = http.DefaultTransport
					policy := "auto"
					alternate, host := "cf", "play.udon.dance"
					if preferred == "cf" {
						alternate, host = "nya", "nya.xin.moe"
					}
					wantNodes := preferred + "," + alternate
					if mode == "fixed" {
						policy = "hkg"
						if preferred == "cf" {
							policy = "cf"
						}
						wantNodes = preferred
					}
					target, err := c.resolvePlayback(context.Background(), "42", preferred, policy)
					if mode == "auto" {
						if err != nil || target != "https://"+host+valid {
							t.Fatalf("target=%q err=%v; want valid alternate", target, err)
						}
						if err := cacheproxy.ValidateVideoURL(target, fixtureCacheConfig().MaxFileBytes); err != nil {
							t.Fatalf("cache rejected alternate: %v", err)
						}
					} else if err == nil || target != "" {
						t.Fatalf("target=%q err=%v; want resolution failure", target, err)
					}
					mu.Lock()
					defer mu.Unlock()
					if got := strings.Join(nodes, ","); got != wantNodes {
						t.Fatalf("queried nodes=%s; want %s", got, wantNodes)
					}
				})
			}
		}
	}
}

func TestPlaybackResolutionPolicyAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, mode, node, failed, wantHost, wantNodes string
	}{
		{"fixed CF default", "cf", "", "nya", "play.udon.dance", "cf"},
		{"fixed CF overrides HKG", "cf", "nya", "nya", "play.udon.dance", "cf"},
		{"fixed HKG overrides CF", "hkg", "cf", "cf", "nya.xin.moe", "nya"},
		{"fixed CF fails without fallback", "cf", "nya", "cf", "", "cf"},
		{"fixed HKG fails without fallback", "hkg", "cf", "nya", "", "nya"},
		{"auto default", "auto", "", "", "nya.xin.moe", "nya"},
		{"auto prefers HKG", "auto", "nya", "", "nya.xin.moe", "nya"},
		{"auto prefers CF", "auto", "cf", "", "play.udon.dance", "cf"},
		{"auto default fallback", "auto", "", "nya", "play.udon.dance", "nya,cf"},
		{"auto HKG fallback", "auto", "nya", "nya", "play.udon.dance", "nya,cf"},
		{"auto CF fallback", "auto", "cf", "cf", "nya.xin.moe", "cf,nya"},
		{"auto both fail", "auto", "", "both", "", "nya,cf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			var mu sync.Mutex
			var nodes []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				node := r.URL.Query().Get("node")
				mu.Lock()
				nodes = append(nodes, node)
				mu.Unlock()
				if r.URL.Path != "/Api/Songs/play" || r.URL.Query().Get("id") != "42" {
					t.Errorf("unexpected API request: %s", r.URL)
				}
				if tc.failed == "both" || tc.failed == node {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				host := "nya.xin.moe"
				if node == "cf" {
					host = "play.udon.dance"
				}
				w.Header().Set("Location", "https://"+host+"/files/1/2-"+node+".mp4?e=00000000000000000000000000000000&s=1")
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			target, err := c.resolvePlayback(context.Background(), "42", tc.node, tc.mode)
			if tc.wantHost == "" {
				if err == nil || target != "" {
					t.Fatalf("target=%q err=%v; expected resolution failure", target, err)
				}
			} else {
				node := "nya"
				if tc.wantHost == "play.udon.dance" {
					node = "cf"
				}
				want := "https://" + tc.wantHost + "/files/1/2-" + node + ".mp4?e=00000000000000000000000000000000&s=1"
				if err != nil || target != want {
					t.Fatalf("target=%q err=%v; want %q", target, err, want)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if got := strings.Join(nodes, ","); got != tc.wantNodes {
				t.Fatalf("queried nodes=%s; want %s", got, tc.wantNodes)
			}
		})
	}
}

func TestPlaybackResolutionDoesNotWaitForUnusedRoute(t *testing.T) {
	for _, node := range []string{"", "nya", "cf"} {
		t.Run("node="+node, func(t *testing.T) {
			c := testConsole(t)
			preferred, host := "nya", "nya.xin.moe"
			if node == "cf" {
				preferred, host = "cf", "play.udon.dance"
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("node") != preferred {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Location", "https://"+host+"/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1")
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			target, err := c.resolvePlayback(ctx, "42", node, "auto")
			if err != nil || target != "https://"+host+"/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1" || ctx.Err() != nil {
				t.Fatalf("preferred route waited for stalled alternative: target=%q err=%v context=%v", target, err, ctx.Err())
			}
		})
	}
}

func TestPlaybackResolutionStalledPreferredRoute(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout falls back", true: "cancellation stops fallback"}[cancelRequest], func(t *testing.T) {
			c := testConsole(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var mu sync.Mutex
			var nodes []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				node := r.URL.Query().Get("node")
				mu.Lock()
				nodes = append(nodes, node)
				mu.Unlock()
				if node == "nya" {
					if cancelRequest {
						cancel()
					}
					<-r.Context().Done()
					return
				}
				w.Header().Set("Location", "https://play.udon.dance/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1")
				w.WriteHeader(http.StatusFound)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			target, err := c.resolvePlayback(ctx, "42", "nya", "auto")
			wantNodes := "nya,cf"
			if cancelRequest {
				wantNodes = "nya"
				if !errors.Is(err, context.Canceled) || target != "" {
					t.Fatalf("target=%q err=%v; want cancellation", target, err)
				}
			} else if err != nil || target != "https://play.udon.dance/files/1/2-video.mp4?e=00000000000000000000000000000000&s=1" {
				t.Fatalf("target=%q err=%v; want timeout fallback", target, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if got := strings.Join(nodes, ","); got != wantNodes {
				t.Fatalf("queried nodes=%s; want %s", got, wantNodes)
			}
		})
	}
}

func TestPlaybackResolutionCancelsBothPendingRoutes(t *testing.T) {
	c := testConsole(t)
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		stopped <- struct{}{}
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.resolvePlayback(ctx, "42", "nya", "auto")
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("alternate route did not start")
		}
	}
	select {
	case <-stopped:
		t.Fatal("starting alternate canceled the preferred route")
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resolution ignored cancellation")
	}
	for range 2 {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("route request survived caller cancellation")
		}
	}
}
