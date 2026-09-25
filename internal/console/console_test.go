package console

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
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

	"stepstash/internal/cacheproxy"
)

func testConsole(t *testing.T) *Console {
	t.Helper()
	root := t.TempDir()
	c, err := New(filepath.Join(root, "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	c.settings = Settings{StorageDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs")}
	c.videoAddress = "127.0.0.1:0"
	t.Cleanup(func() { c.Close() })
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
	for _, data := range []string{"1.2.3.4 play.udon.dance", "::1 nya.xin.moe", "127.0.0.1 api.udon.dance"} {
		if _, err := transformHosts(data, "enable"); err == nil {
			t.Fatal("accepted conflict", data)
		}
	}
	changed := strings.Replace(enabled, "127.0.0.1 nya.xin.moe", "1.2.3.4 nya.xin.moe", 1)
	if _, err := transformHosts(changed, "disable"); err == nil {
		t.Fatal("silently removed edited mapping")
	}
}

func TestSettingsPersistAndPortConflict(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	other, err := New(c.configPath, c.address)
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
	if err = c.save(c.settings); err == nil {
		t.Fatal("changed active settings")
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
		r := httptest.NewRequest("POST", "http://"+tc.host+"/api/start", nil)
		r.Header.Set("X-StepStash-Token", tc.token)
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
		if r.Host != "nya.xin.moe" {
			t.Error("upstream Host not preserved")
		}
		io.WriteString(w, body)
	}))
	defer origin.Close()
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	cfg.Origins["nya.xin.moe"] = strings.TrimPrefix(origin.URL, "http://")
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Api/Songs/list" {
			io.WriteString(w, `{"groups":{"contents":[{"songInfos":[{"id":1,"name":"first"},{"id":1,"name":"duplicate"},{"id":2,"name":"unavailable"}]}]}}`)
			return
		}
		if r.URL.Query().Get("id") == "2" {
			http.Error(w, "unavailable", 404)
			return
		}
		if r.URL.Query().Get("node") != "nya" {
			t.Error("HKG must use the observed node=nya protocol parameter")
			http.Error(w, "unsupported node", 400)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("http://nya.xin.moe/files/2403/1-abc.mp4?e=%s&s=%d", digest, len(body)))
		w.WriteHeader(302)
	}))
	defer api.Close()
	c.apiBase = api.URL
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
	b, err := os.ReadFile(fixtureVideoPath(c.settings.StorageDir, "1", body))
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

func TestCatalogAndDNSValidation(t *testing.T) {
	for _, input := range []string{`{}`, `{"groups":{"contents":[]}}`, `not-json`} {
		if _, err := parseCatalog(strings.NewReader(input)); err == nil {
			t.Fatal("accepted invalid catalog")
		}
	}
	var answer dnsAnswer
	if err := json.Unmarshal([]byte(`{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"127.0.0.1"},{"type":1,"TTL":120,"data":"203.0.113.9"}]}`), &answer); err != nil {
		t.Fatal(err)
	}
	ips, ttl, err := dnsAddresses(answer)
	if err != nil || len(ips) != 1 || ips[0] != "203.0.113.9" || ttl != 120*time.Second {
		t.Fatal(ips, ttl, err)
	}
	// An unresolvable OS name still connects via the independent resolver cache.
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	d := &directDNS{cache: map[string]dnsEntry{"hosts-must-not-be-used.invalid": {[]string{"127.0.0.1"}, time.Now().Add(time.Minute)}}}
	conn, err := d.DialContext(context.Background(), "tcp", "hosts-must-not-be-used.invalid:"+port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestLiveIndependentDNS(t *testing.T) {
	if os.Getenv("STEPSTASH_LIVE_CHECK") != "1" {
		t.Skip("opt-in network check")
	}
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	songs, err := c.catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog: %d unique songs", len(songs))
	target, err := c.resolve(ctx, 1343)
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
