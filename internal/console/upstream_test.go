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

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestPrefetchUpstreamSelectionAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, mode, failure, nodes string
		wantErr                    bool
	}{
		{"auto success", "auto", "", "cf", false},
		{"auto status fallback", "auto", "status", "cf,nya", false},
		{"auto checksum fallback", "auto", "checksum", "cf,nya", false},
		{"auto timeout fallback", "auto", "timeout", "cf,nya", false},
		{"auto API fallback", "auto", "api", "cf,nya", false},
		{"both fail", "auto", "both", "cf,nya", true},
		{"fixed HKG failure", "hkg", "status", "nya", true},
		{"HKG returns CF resource", "hkg", "hkg-cf", "nya", false},
		{"same URL is not another resource fallback", "auto", "shared-status", "cf,nya", true},
		{"fixed CF success", "cf", "", "cf", false},
		{"fixed CF failure", "cf", "both", "cf", true},
		{"CF returns nya resource", "cf", "wrong-host", "cf", false},
		{"cancel stops fallback", "auto", "cancel", "cf", true},
		{"removed stops fallback", "auto", "removed", "cf", true},
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
				if r.Host == "play.udon.dance" || tc.mode == "hkg" {
					switch tc.failure {
					case "status", "shared-status":
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
				if tc.failure == "api" && node == "cf" {
					http.Error(w, "failed", 503)
					return
				}
				host := "nya.xin.moe"
				if (node == "cf" && tc.failure != "wrong-host") || tc.failure == "hkg-cf" || tc.failure == "shared-status" {
					host = "play.udon.dance"
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/138-abc.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase = api.URL
			c.client.Transport = http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.OriginScheme = "http"
			cfg.StorageDir = c.settings.StorageDir
			if tc.failure == "timeout" {
				// The budget covers SQLite and file publication as well as HTTP.
				// Leave room for a healthy fallback on slower Windows CI disks.
				cfg.DownloadTimeout = 3 * time.Second
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
			if tc.failure == "shared-status" {
				mu.Lock()
				count := len(hosts)
				mu.Unlock()
				if count != 1 {
					t.Fatalf("same returned URL downloaded %d times", count)
				}
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

func TestResolvingSameURLPreservesIssuingNodes(t *testing.T) {
	c := testConsole(t)
	target := "http://nya.xin.moe/files/1/2-video.mp4?e=28711962048bed664c98f27e1d9d5842&s=4"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.client.Transport = http.DefaultTransport
	for _, node := range []string{"cf", "hkg"} {
		got, err := c.resolveNode(context.Background(), 42, node)
		if err != nil || got != target {
			t.Fatal(got, err)
		}
	}
	var sources []upstreamrequest.ResourceSource
	for _, s := range upstreamrequest.Default.ResourceSources("nya.xin.moe") {
		if strings.HasPrefix(s.API, api.URL+"/") {
			sources = append(sources, s)
		}
	}
	if len(sources) != 2 {
		t.Fatal("dedup discarded provenance", sources)
	}
	for _, s := range sources {
		if s.ResourceURL != target || s.SongID != 42 || !strings.Contains(s.API, "node="+s.Node) {
			t.Fatal(s)
		}
	}
	if sources[0].Node != "cf" || sources[1].Node != "nya" {
		t.Fatal("domain inferred a node", sources)
	}
}
