package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Uses real console routes, SQLite, resource files and an engine reopen. No
// production consent receipt, game, hosts mutation or external origin is used.
func TestSongPreferencesBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_SONG_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_SONG_BROWSER_EVIDENCE for real console browser verification")
	}
	c := testConsole(t)
	var resolutions atomic.Int32
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.CacheUnlimited = false
	cfg.MaxCacheBytes = 0
	cfg.AggressiveCacheReclamation = "off"
	cfg.ResolvePlayback = func(context.Context, string, string) (string, error) {
		resolutions.Add(1)
		return "", fmt.Errorf("unexpected resolution")
	}
	seedConfig := cfg
	seedConfig.CacheUnlimited = true
	engine, err := cacheproxy.New(seedConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	name1, name2, name3 := "未下载的收藏测试", "屏蔽测试", "已保存的收藏测试"
	key := fmt.Sprintf("%x", md5.Sum([]byte("saved")))
	if err := engine.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261021000000", Source: "test", Songs: []cacheproxy.CatalogSong{
		{ID: 1, Name: &name1, MD5: strings.Repeat("a", 32)},
		{ID: 2, Name: &name2, MD5: strings.Repeat("b", 32)},
		{ID: 3, Name: &name3, MD5: key},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.VideoDir, key+".mp4"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__preferences/reopen":
			c.lifecycleMu.Lock()
			defer c.lifecycleMu.Unlock()
			c.mu.Lock()
			old := c.service
			c.mu.Unlock()
			if old != nil {
				if err := old.Close(); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
			fresh, err := cacheproxy.New(cfg)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			c.mu.Lock()
			c.service = fresh
			c.mu.Unlock()
			writeJSON(w, map[string]bool{"ok": true, "wasRunning": old != nil})
			return
		case "/__preferences/probe":
			c.mu.Lock()
			current := c.service
			c.mu.Unlock()
			req := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=2", nil)
			response := httptest.NewRecorder()
			current.ServeHTTP(response, req)
			writeJSON(w, map[string]int{"status": response.Code, "resolutions": int(resolutions.Load())})
			return
		}
		c.ServeHTTP(w, r)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "song-preferences-live.mjs"), ui.URL, evidence)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if resolutions.Load() != 0 || c.batch.Running || c.queue.Running || c.httpServer != nil {
		t.Fatal("preferences started network or service activity", resolutions.Load())
	}
	policies, err := c.service.SongPolicies(t.Context())
	if err != nil || policies[1] != cacheproxy.SongFavorite || policies[2] != cacheproxy.SongBlocked || policies[3] != cacheproxy.SongFavorite || policies[314] != cacheproxy.SongBlocked {
		t.Fatal(policies, err)
	}
}
