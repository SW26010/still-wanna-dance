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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Opt-in because the Go suite must not require Node or an installed browser.
// Unlike the memory UI fixture, this uses real console HTTP/SSE routes, the
// scheduler, a controlled loopback Range origin, and actual temporary files.
func TestBlockTimelineBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_TIMELINE_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_TIMELINE_BROWSER_EVIDENCE to run isolated browser integration")
	}
	c := testConsole(t) // isolated test consent; never reads the user's receipt
	body := strings.Repeat("b", 3*1048576+37)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			http.Error(w, "range required", 400)
			return
		}
		last = min(last, int64(len(body)-1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(last-first+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		select {
		case <-gate:
			io.WriteString(w, body[first:last+1])
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(origin.Close)
	cfg := cacheproxy.DefaultConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.OriginScheme = "http"
	cfg.Origins = map[string]string{"play.udon.dance": strings.TrimPrefix(origin.URL, "http://")}
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := cacheproxy.New(cfg)
	if err != nil {
		release()
		t.Fatal(err)
	}
	c.service = service
	proxy := httptest.NewServer(service)
	t.Cleanup(proxy.Close)
	var streams atomic.Int32
	ui := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__timeline/release":
			release()
			writeJSON(w, map[string]bool{"ok": true})
			return
		case "/__timeline/streams":
			writeJSON(w, streams.Load())
			return
		case "/api/block-demands/events":
			streams.Add(1)
			defer streams.Add(-1)
		}
		c.ServeHTTP(w, r)
	}))
	c.address = ui.Listener.Addr().String()
	ui.Start()
	t.Cleanup(ui.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(func() { release(); cancel(); service.Close() })
	download := make(chan error, 1)
	go func() {
		target := fmt.Sprintf("%s/files/2403/1344-660524b4ebadb.mp4?e=%x&s=%d", proxy.URL, md5.Sum([]byte(body)), len(body))
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err == nil {
			req.Host = "play.udon.dance"
			var response *http.Response
			response, err = http.DefaultClient.Do(req)
			if err == nil {
				var received []byte
				received, err = io.ReadAll(response.Body)
				response.Body.Close()
				if err == nil && (response.StatusCode != 200 || string(received) != body) {
					err = fmt.Errorf("download mismatch: status %d, bytes %d", response.StatusCode, len(received))
				}
			}
		}
		download <- err
	}()
	command := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "block-timeline-live.mjs"), ui.URL, evidence)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	select {
	case err := <-download:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if streams.Load() != 0 {
		t.Fatal("browser left SSE requests open", streams.Load())
	}
}
