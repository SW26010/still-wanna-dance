package console

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamnet"
)

// Tests can exercise the gateway without requesting system hosts access.
func (c *Console) start() (err error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startLocked()
}

func testWorkDirectory(t *testing.T) *cacheproxy.WorkDirectory {
	t.Helper()
	work, err := cacheproxy.OpenWorkDirectory(filepath.Join(t.TempDir(), "work"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := work.Close(); err != nil {
			t.Error(err)
		}
	})
	return work
}

func newConsoleWithWorkForTest(t *testing.T, path, address string) (*Console, error) {
	t.Helper()
	// General fixtures exercise storage without the distribution's 10k-row seed.
	// Production bootstrap is covered separately through New.
	return newConsole(path, address, upstreamnet.NewResolver(nil), testWorkDirectory(t))
}

func testConsole(t *testing.T) *Console {
	t.Helper()
	root := t.TempDir()
	store, err := cacheproxy.CreateStore(filepath.Join(root, "library", "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "library", "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(fmt.Sprintf("{\"schemaVersion\":%d,\"databasePath\":\"library/storage.sqlite\",\"videoDir\":\"library/videos\"}", configSchemaVersion)), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := newConsole(filepath.Join(root, "config.json"), "127.0.0.1:18081", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.hosts, _ = fixtureHostsController(t, hostsBaseline)
	c.checksumURL = ""
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	setTestLibraryPaths(&c.settings, filepath.Join(root, "library"))
	c.settings.VRChatLogDir = filepath.Join(root, "logs")
	c.savedSettings = c.settings
	c.videoAddress = "127.0.0.1:0"
	c.httpsAddress = "127.0.0.1:0"
	return c
}

func TestHostsRoundTripAndConflicts(t *testing.T) {
	original := "# user configuration\r\n127.0.0.1 localhost\r\n127.0.0.1 play.udon.dance # user's mapping\r\n"
	enabled, err := transformHosts(original, "enable")
	if err != nil {
		t.Fatal(err)
	}
	if !inspectHosts(enabled).Ready {
		t.Fatal(enabled)
	}
	again, err := transformHosts(enabled, "enable")
	if err != nil || again != enabled {
		t.Fatal("enable not idempotent", err)
	}
	restored, err := transformHosts(enabled, "disable")
	if err != nil || restored != original {
		t.Fatalf("restore altered user entries: %q %v", restored, err)
	}
	restored, err = transformHosts(restored, "disable")
	if err != nil || restored != original {
		t.Fatal("restore not idempotent")
	}
	for _, data := range []string{"::1 api.udon.dance", "1.2.3.4 api.udon.dance"} {
		if _, err := transformHosts(data, "enable"); err == nil {
			t.Fatal("accepted conflict", data)
		}
	}
	changed := strings.Replace(enabled, "127.0.0.1 api.udon.dance", "1.2.3.4 api.udon.dance", 1)
	if _, err := transformHosts(changed, "disable"); err == nil {
		t.Fatal("silently removed edited mapping")
	}
}

func TestSettingsPersistAndPortConflict(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	other, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.settings != c.settings {
		t.Fatal("settings not persisted")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c.videoAddress = l.Addr().String()
	if err = c.start(); err == nil {
		t.Fatal("occupied port accepted")
	}
	if c.httpServer != nil {
		t.Fatal("false running state")
	}
	c.videoAddress = "127.0.0.1:0"
	if err = c.start(); err != nil {
		t.Fatal(err)
	}
	if err = c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	if err = c.stop(); err != nil {
		t.Fatal(err)
	}
	if c.httpServer != nil {
		t.Fatal("listener remains")
	}
	if err = c.save(c.settings); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserWriteProtection(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct{ host, token, origin string }{{"evil.example", c.token, ""}, {c.address, "", ""}, {c.address, c.token, "https://evil.example"}} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/api/activation/enable", nil)
		r.Header.Set("X-SWD-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/", nil)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), c.token) {
		t.Fatal("page missing token")
	}
}

func TestBatchWorksWithoutCDNAndReusesCache(t *testing.T) {
	c := testConsole(t)
	body := "a complete verified video fixture"
	sum := md5.Sum([]byte(body))
	digest := hex.EncodeToString(sum[:])
	var downloads atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Host != "play.udon.dance" {
			t.Error("upstream Host not preserved")
		}
		io.WriteString(w, body)
	}))
	defer origin.Close()
	cfg := fixtureCacheConfig()
	cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
	cfg.Origins["play.udon.dance"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			writeTestMD5Catalog(w, map[int]string{1: body, 2: "unavailable"})
			return
		}
		if r.URL.Query().Get("id") == "2" {
			http.Error(w, "unavailable", 404)
			return
		}
		if r.URL.Query().Get("node") != "cf" {
			t.Error("Auto must prefer node=cf")
			http.Error(w, "unsupported node", 400)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://play.udon.dance/files/2403/1-abc.mp4?e=%s&s=%d", digest, len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase = api.URL
	c.checksumURL = api.URL + "/catalog"
	c.client.Transport = http.DefaultTransport
	for run := 0; run < 2; run++ {
		if err = c.startBatch(); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		done := c.batchDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("batch timed out")
		}
		c.mu.Lock()
		batch := c.batch
		running := c.httpServer != nil
		c.mu.Unlock()
		if running || batch.Total != 2 || batch.Checked != 2 || batch.Failed != 1 {
			t.Fatalf("unexpected batch: %+v", batch)
		}
		if run == 0 && batch.Downloaded != 1 {
			t.Fatal(batch)
		}
		if run == 1 && batch.Hits != 1 {
			t.Fatal(batch)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloaded %d times", downloads.Load())
	}
	b, err := os.ReadFile(fixtureVideoPath(testStorageRoot(c.settings), "1", body))
	if err != nil || string(b) != body {
		t.Fatal("cache not published", err)
	}
}

func TestCancelBatchAndStopCDNAreIndependent(t *testing.T) {
	c := testConsole(t)
	started := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer api.Close()
	c.apiBase = api.URL
	c.checksumURL = api.URL + "/catalog"
	c.client.Transport = http.DefaultTransport
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	running := c.batch.Running
	done := c.batchDone
	c.batchCancel()
	c.mu.Unlock()
	if !running {
		t.Fatal("stopping CDN stopped independent batch")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt catalog request")
	}
}

func TestLiveIndependentDNS(t *testing.T) {
	if os.Getenv("SWD_LIVE_CHECK") != "1" {
		t.Skip("opt-in network check")
	}
	c := testConsole(t)
	c.checksumURL = "https://x.kiva.moe/api/v2/wanna/songs"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	catalog, err := c.fetchCatalogSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog: %d unique songs", len(catalog.Songs))
	target, err := c.resolveSongURL(ctx, 1343)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Range", "bytes=0-15")
	resp, err := c.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 17))
	if err != nil || resp.StatusCode != 206 || len(b) != 16 {
		t.Fatal(resp.StatusCode, len(b), err)
	}
	t.Log("API and video range request succeeded using independent DNS")
}
