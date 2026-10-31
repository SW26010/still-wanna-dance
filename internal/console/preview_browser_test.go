package console

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestPreviewBrowser(t *testing.T) {
	evidence, videoPath := os.Getenv("SWD_PREVIEW_BROWSER_EVIDENCE"), os.Getenv("SWD_PREVIEW_VIDEO")
	if evidence == "" || videoPath == "" {
		t.Skip("set SWD_PREVIEW_BROWSER_EVIDENCE and SWD_PREVIEW_VIDEO for isolated real-media browser validation")
	}
	body, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	c := testConsole(t)
	defer c.Close()
	c.settings.VideoStorageMode = "unlimited"
	c.savedSettings = c.settings
	key := fmt.Sprintf("%x", md5.Sum(body))
	var downloads atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		http.ServeContent(w, r, "synthetic.mp4", time.Time{}, bytes.NewReader(body))
	}))
	defer origin.Close()
	cfg := fixtureCacheConfig()
	cfg.CacheUnlimited = true
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, c.settings.VideoDir
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.BeginVideoRequest = c.beginVideoRequest
	for host := range cfg.Origins {
		cfg.Origins[host] = strings.TrimPrefix(origin.URL, "http://")
	}
	cfg.ResolvePlayback = func(ctx context.Context, id string) (string, error) {
		target := fmt.Sprintf("http://play.udon.dance/files/2403/1-abc.mp4?e=%s&s=%d", key, len(body))
		n, _ := strconv.ParseInt(id, 10, 64)
		now := time.Now()
		err := c.store.ObserveSongURL(ctx, cacheproxy.SongURL{SongID: n, URL: target, API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: now, ObservedAt: now})
		return target, err
	}
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	name := "合成预览"
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261027000000", Songs: []cacheproxy.CatalogSong{{ID: 1, Name: &name, MD5: key}, {ID: 2, Name: &name, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "preview-live.mjs"), ui.URL, key, evidence)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	page, err := cacheproxy.ReadLibraryPage(ctx, libraryPaths(c.settings), cacheproxy.LibraryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, song := range page.Songs {
		if song.HasDemand {
			t.Fatal("preview changed demand score", song)
		}
	}
	recent, err := cacheproxy.ReadRecentRequests(ctx, c.settings.DatabasePath, 50)
	if err != nil || len(recent.Requests) != 0 {
		t.Fatal("preview entered history", recent, err)
	}
	if downloads.Load() != 1 {
		t.Fatal("cached preview redownloaded", downloads.Load())
	}
	if c.httpServer != nil || c.queue.Running || !c.activation.FirstRequest.IsZero() {
		t.Fatal("preview changed game state")
	}
	t.Logf("decoded synthetic MP4, seek/pause/replay/navigation/cross-origin passed; upstream requests=%d", downloads.Load())
}
