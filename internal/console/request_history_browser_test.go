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
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Real request processing, SQLite reopen, Console HTTP and Edge. All origins
// and storage are private test fixtures; no gateway/hosts/game startup.
func TestRequestHistoryBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_HISTORY_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_HISTORY_BROWSER_EVIDENCE for real history browser integration")
	}
	c := testConsole(t)
	body := "history browser video"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		io.WriteString(w, body)
	}))
	defer origin.Close()
	cfg := fixtureCacheConfig()
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	cfg.CacheUnlimited = true
	cfg.RequestRetentionDays, cfg.RequestRetentionUnlimited = 30, false
	target := fmt.Sprintf("http://play.udon.dance/files/2403/42-a.mp4?e=%s&s=%d", key, len(body))
	cfg.ResolvePlayback = func(context.Context, string) (string, error) { return target, nil }
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for i := 0; i < 601; i++ {
		r := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id=42", nil)
		if i > 0 {
			r.Header.Set("Range", "bytes=0-3")
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 206 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Current mapping changes without rewriting the already handled request.
	name := "new current mapping"
	if err := engine.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261024000000", Source: "test", Songs: []cacheproxy.CatalogSong{{ID: 42, Name: &name, MD5: strings.Repeat("b", 32)}}}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err = cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/__fixture/request" {
			c.ServeHTTP(w, r)
			return
		}
		// Trigger real traffic only after the browser has subscribed. Closing
		// the fixture engine drains its accepted SQLite writes before replying.
		writer, err := cacheproxy.New(cfg)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		count, songID := 1, "84"
		if r.URL.Query().Get("pair") == "true" {
			count, songID = 2, "85"
		}
		var response *httptest.ResponseRecorder
		for i := 0; i < count; i++ {
			response = httptest.NewRecorder()
			request := httptest.NewRequest("GET", "http://api.udon.dance/Api/Songs/play?id="+songID, nil)
			if count == 2 && i == 0 {
				ctx, cancel := context.WithCancel(request.Context())
				writer.ServeHTTP(&historyCanceledWriter{response, cancel}, request.WithContext(ctx))
				cancel()
			} else {
				writer.ServeHTTP(response, request)
			}
		}
		if err := writer.Close(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(response.Code)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "history-live.mjs"), ui.URL, key, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("read-only history started engine or gateway")
	}
}

type historyCanceledWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *historyCanceledWriter) Write(p []byte) (int, error) {
	w.cancel()
	return w.ResponseRecorder.Write(p)
}
