package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCurrentVersionFollowsConfiguredAuthority(t *testing.T) {
	const old, fresh = "old version", "new version"
	for _, tc := range []struct {
		name, mode, cf, hkg, want, node string
	}{
		{"cf-ahead", "cf", fresh, old, fresh, "cf"},
		{"hkg-ahead", "hkg", old, fresh, fresh, "nya"},
		{"cf-unavailable", "cf", "", fresh, old, "cf"},
		{"auto-cf-ahead", "auto", fresh, old, old, "nya"},
		{"auto-hkg-ahead", "auto", old, fresh, fresh, "nya"},
		{"auto-hkg-unavailable", "auto", fresh, "", old, "nya"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			c.settings.DownloadUpstream = tc.mode
			c.settings.MaxCacheBytes = 0
			target := func(host, body string) string {
				return fmt.Sprintf("http://%s/files/2403/42-abc.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body))
			}
			var offline atomic.Bool
			var confirmations atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if offline.Load() {
					http.Error(w, "offline", 503)
					return
				}
				confirmations.Add(1)
				node := r.URL.Query().Get("node")
				if node != tc.node {
					t.Errorf("confirmation queried %s, want %s", node, tc.node)
				}
				body, host := tc.hkg, "nya.xin.moe"
				if node == "cf" {
					body, host = tc.cf, "play.udon.dance"
				}
				if body == "" {
					http.Error(w, "unavailable", 503)
					return
				}
				w.Header().Set("Location", target(host, body))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			start := func() {
				t.Helper()
				c.lifecycleMu.Lock()
				c.mu.Lock()
				err := c.ensureEngine()
				c.mu.Unlock()
				c.lifecycleMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			start()
			// Supply verified local bytes to exercise the production engine wiring
			// and mapping/cleanup path without contacting a real video origin.
			for _, body := range []string{old, fresh} {
				path := fixtureVideoPath(c.settings.StorageDir, "42", body)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if source, err := c.service.PrefetchSong(context.Background(), "42", target("play.udon.dance", body)); err != nil || source != "HIT" {
					t.Fatalf("prefetch: %s, %v", source, err)
				}
			}
			if confirmations.Load() != 1 {
				t.Fatalf("got %d confirmation queries", confirmations.Load())
			}
			// Close drains asynchronous cleanup even with an unlimited cache.
			if err := c.service.Close(); err != nil {
				t.Fatal(err)
			}
			c.service = nil
			for _, body := range []string{old, fresh} {
				hit, err := cacheproxy.CheckLocal(context.Background(), c.settings.StorageDir, target("play.udon.dance", body))
				if err != nil || hit != (body == tc.want) {
					t.Fatalf("retained %q: %v, %v; want %q", body, hit, err, tc.want)
				}
			}
			offline.Store(true)
			start()
			w := httptest.NewRecorder()
			c.service.ServeHTTP(w, httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42&node=cf", nil))
			if w.Code != http.StatusOK || w.Body.String() != tc.want || w.Header().Get("X-StepStash-Fallback") != "upstream-unavailable" {
				t.Fatalf("offline playback: %d %q %v", w.Code, w.Body.String(), w.Header())
			}
		})
	}
}
