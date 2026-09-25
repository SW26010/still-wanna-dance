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
	"testing"
	"time"

	"stepstash/internal/cacheproxy"
)

func TestPrefetchUpstreamSelectionAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, mode, failure, nodes string
		wantErr                    bool
	}{
		{"auto success", "auto", "", "nya", false},
		{"auto status fallback", "auto", "status", "nya,cf", false},
		{"auto checksum fallback", "auto", "checksum", "nya,cf", false},
		{"auto timeout fallback", "auto", "timeout", "nya,cf", false},
		{"auto API fallback", "auto", "api", "nya,cf", false},
		{"both fail", "auto", "both", "nya,cf", true},
		{"fixed HKG failure", "hkg", "status", "nya", true},
		{"fixed CF success", "cf", "", "cf", false},
		{"fixed CF failure", "cf", "both", "cf", true},
		{"wrong route", "cf", "wrong-host", "cf", true},
		{"cancel stops fallback", "auto", "cancel", "nya", true},
		{"removed stops fallback", "auto", "removed", "nya", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			c.settings.DownloadUpstream = tc.mode
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := "verified upstream fixture"
			var mu sync.Mutex
			var nodes, hosts []string
			wanted := true
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hosts = append(hosts, r.Host)
				mu.Unlock()
				if tc.failure == "both" {
					http.Error(w, "failed", 503)
					return
				}
				if r.Host == "nya.xin.moe" {
					switch tc.failure {
					case "status":
						http.Error(w, "failed", 503)
						return
					case "checksum":
						io.WriteString(w, strings.Repeat("x", len(body)))
						return
					case "timeout":
						<-r.Context().Done()
						return
					case "cancel":
						cancel()
						<-r.Context().Done()
						return
					case "removed":
						mu.Lock()
						wanted = false
						mu.Unlock()
						http.Error(w, "failed", 503)
						return
					}
				}
				io.WriteString(w, body)
			}))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				node := r.URL.Query().Get("node")
				mu.Lock()
				nodes = append(nodes, node)
				mu.Unlock()
				if tc.failure == "api" && node == "nya" {
					http.Error(w, "failed", 503)
					return
				}
				host := "nya.xin.moe"
				if node == "cf" && tc.failure != "wrong-host" {
					host = "play.udon.dance"
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/1-abc.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := cacheproxy.DefaultConfig()
			cfg.StorageDir = c.settings.StorageDir
			if tc.failure == "timeout" {
				cfg.DownloadTimeout = 150 * time.Millisecond
			}
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			source, err := c.prefetchSong(ctx, engine, 1, func() bool { mu.Lock(); defer mu.Unlock(); return wanted })
			if (err != nil) != tc.wantErr {
				t.Fatalf("source=%s err=%v", source, err)
			}
			if tc.failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if tc.failure == "removed" && !errors.Is(err, errSongRemoved) {
				t.Fatal(err)
			}
			if tc.failure == "both" && tc.mode == "auto" && (!strings.Contains(err.Error(), "hkg") || !strings.Contains(err.Error(), "cf")) {
				t.Fatal(err)
			}
			mu.Lock()
			got := strings.Join(nodes, ",")
			mu.Unlock()
			if got != tc.nodes {
				t.Fatalf("nodes=%s want=%s", got, tc.nodes)
			}
			if !tc.wantErr {
				if source != "MISS" {
					t.Fatal(source)
				}
				// A repeated request uses the verified shared cache without a new origin download.
				mu.Lock()
				count := len(hosts)
				mu.Unlock()
				if source, err = c.prefetchSong(ctx, engine, 1, nil); err != nil || source != "HIT" {
					t.Fatalf("%s %v", source, err)
				}
				mu.Lock()
				after := len(hosts)
				mu.Unlock()
				if after != count {
					t.Fatalf("cache redownloaded: %d -> %d", count, after)
				}
			}
		})
	}
}

func TestDownloadUpstreamSettings(t *testing.T) {
	c := testConsole(t)
	s, err := absoluteSettings(c.settings)
	if err != nil || s.DownloadUpstream != "auto" {
		t.Fatalf("legacy default: %+v %v", s, err)
	}
	for _, mode := range []string{"auto", "cf", "hkg"} {
		s.DownloadUpstream = mode
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.settings.DownloadUpstream != mode {
			t.Fatal(loaded.settings)
		}
		loaded.Close()
	}
	s.DownloadUpstream = "invalid"
	if err := c.save(s); err == nil {
		t.Fatal("accepted invalid upstream")
	}
}
