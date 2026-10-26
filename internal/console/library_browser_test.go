package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"log/slog"
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

func TestLibraryBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_LIBRARY_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_LIBRARY_BROWSER_EVIDENCE for real library browser integration")
	}
	c := testConsole(t)
	defer c.Close()
	c.settings.VideoStorageMode = "unlimited"
	c.savedSettings = c.settings
	const body = "library browser video"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	var downloads atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); fmt.Fprint(w, body) }))
	defer origin.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/1-abc.mp4?e=%s&s=%d", key, len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	cfg := fixtureCacheConfig()
	cfg.OriginScheme, cfg.CacheUnlimited = "http", true
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.LocalProtectionGrace = 0
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	name := "共享视频"
	if err := engine.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261024000000", Songs: []cacheproxy.CatalogSong{{ID: 1, Name: &name, MD5: key}, {ID: 2, Name: &name, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "library-live.mjs"), ui.URL, key, evidence)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	if downloads.Load() != 1 {
		t.Fatal("shared resource downloaded more than once", downloads.Load())
	}
	if _, err := os.Stat(filepath.Join(cfg.VideoDir, key+".mp4")); !os.IsNotExist(err) {
		t.Fatal("browser deletion did not remove file", err)
	}
	if c.httpServer != nil || c.queue.Running {
		t.Fatal("library started gateway/queue")
	}
}
