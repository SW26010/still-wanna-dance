package console

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
)

func TestPrefetchOnlyUsesCFEntry(t *testing.T) {
	for _, failure := range []string{"", "status", "checksum", "timeout", "api", "cancel", "other-domain"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const body = "verified upstream fixture"
			var mu sync.Mutex
			var nodes []string
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch failure {
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
				}
				io.WriteString(w, body)
			}))
			defer origin.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				node := r.URL.Query().Get("node")
				mu.Lock()
				nodes = append(nodes, node)
				mu.Unlock()
				if failure == "api" {
					http.Error(w, "failed", 503)
					return
				}
				host := "play.udon.dance"
				if failure == "other-domain" {
					host = "nya.xin.moe"
				}
				w.Header().Set("Location", fmt.Sprintf("http://%s/files/2403/138-abc.mp4?e=%x&s=%d", host, md5.Sum([]byte(body)), len(body)))
				w.WriteHeader(302)
			}))
			defer api.Close()
			c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
			cfg := fixtureCacheConfig()
			cfg.OriginScheme = "http"
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			if failure == "timeout" {
				cfg.DownloadTimeout = 100 * time.Millisecond
			}
			for host := range cfg.Origins {
				cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
			}
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			source, err := c.prefetchSong(ctx, engine, 1)
			wantErr := failure != "" && failure != "other-domain"
			if (err != nil) != wantErr {
				t.Fatalf("source=%s err=%v", source, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			mu.Lock()
			got := strings.Join(nodes, ",")
			mu.Unlock()
			if got != "cf" {
				t.Fatalf("queried nodes=%s; want only cf", got)
			}
			if !wantErr && source != "MISS" {
				t.Fatal(source)
			}
		})
	}
}

func TestObsoleteDownloadUpstreamSettingIsIgnored(t *testing.T) {
	for _, mode := range []string{"auto", "cf", "hkg", "future-node"} {
		t.Run(mode, func(t *testing.T) {
			c := testConsole(t)
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			fields["downloadUpstream"], _ = json.Marshal(mode)
			data, _ = json.Marshal(fields)
			if err := os.WriteFile(c.configPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := newConsoleWithWorkForTest(t, c.configPath, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			if loaded.settings != c.settings {
				t.Fatal("obsolete field changed current settings")
			}
			if err := loaded.save(loaded.settings); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(c.configPath)
			if err != nil || strings.Contains(string(data), "downloadUpstream") {
				t.Fatalf("obsolete selection persisted: %s %v", data, err)
			}
		})
	}
}

func TestCFResolutionPreservesReturnedDomainAndSource(t *testing.T) {
	c := testConsole(t)
	target := "http://nya.xin.moe/files/1/2-video.mp4?e=28711962048bed664c98f27e1d9d5842&s=4"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "cf" {
			t.Errorf("queried %s", r.URL)
		}
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	got, err := c.resolveSongURL(context.Background(), 42)
	if err != nil || got != target {
		t.Fatal(got, err)
	}
	var sources []upstreamrequest.ResourceSource
	for _, s := range upstreamrequest.Default.ResourceSources("nya.xin.moe") {
		if strings.HasPrefix(s.API, api.URL+"/") {
			sources = append(sources, s)
		}
	}
	if len(sources) != 1 || sources[0].Node != "cf" || sources[0].ResourceURL != target || sources[0].SongID != 42 {
		t.Fatal("incorrect provenance", sources)
	}
}
