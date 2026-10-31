package console

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCatalogInitializationPublishesFirstCoverage(t *testing.T) {
	c := testConsole(t)
	var catalogCalls atomic.Int32
	transport := catalogTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":200,"data":{"time":"20261029000000","groups":[{"entries":[{"id":42,"checksum":"0123456789abcdef0123456789abcdef"}]}]}}`
		switch r.URL.Path {
		case "/Api/Songs/list":
			body = `{"time":"20261029000000","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`
		case "/api/v2/wanna/songs", "/api/wannaInfo":
		default:
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		catalogCalls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	c.client.Transport = noCandidatesTransport{transport}
	c.settings.UpstreamMode = "proxy"
	if err := c.StartUpstreamServices(); err != nil {
		t.Fatal(err)
	}
	waitCatalogSync(t, c)
	v := waitInventory(t, c)
	if !v.CoverageKnown || v.TotalSongs != 1 || v.CoveredSongs != 0 || v.Updated.IsZero() {
		t.Fatalf("first successful automatic catalog did not publish coverage: %+v", v)
	}
	if catalogCalls.Load() != 3 || c.service != nil || c.monitor != nil || testDNS(c).queries.Load() != 0 {
		t.Fatalf("catalog initialization escaped its scope: calls=%d engine=%v monitor=%v DNS=%d", catalogCalls.Load(), c.service != nil, c.monitor != nil, testDNS(c).queries.Load())
	}
}

func TestCatalogInitializationRequiresCompleteCatalogAndRetainsSnapshot(t *testing.T) {
	c := testConsole(t)
	if err := c.store.ApplyBundledMediaSeed(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.catalogReady(t.Context()) {
		t.Fatal("seed mappings counted as a complete catalog")
	}
	c.inventoryMu.Lock()
	started := c.inventoryDone != nil
	c.inventoryMu.Unlock()
	if started {
		t.Fatal("seed-only store started a coverage scan")
	}
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20991029000000", Source: cacheproxy.KivaCatalogURL, Songs: []cacheproxy.CatalogSong{{ID: 9000001, MD5: strings.Repeat("a", 32)}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.settings.VideoDir, strings.Repeat("a", 32)+".mp4"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if !c.catalogReady(t.Context()) {
		t.Fatal("accepted catalog did not complete initialization")
	}
	v := waitInventory(t, c)
	if !v.CoverageKnown || v.TotalSongs != 1 || v.CoveredSongs != 1 {
		t.Fatal(v)
	}
	if err := os.Remove(filepath.Join(c.settings.VideoDir, strings.Repeat("a", 32)+".mp4")); err != nil {
		t.Fatal(err)
	}
	if !c.catalogReady(t.Context()) {
		t.Fatal("catalog readiness depended on local video")
	}
	if got := waitInventory(t, c); !got.Updated.Equal(v.Updated) || got.CoveredSongs != v.CoveredSongs {
		t.Fatal("repeated readiness check rescanned established inventory")
	}
	if c.service != nil {
		t.Fatal("initialization started video management")
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if !c.catalogReady(t.Context()) {
		t.Fatal("restart lost accepted catalog")
	}
	if got := waitInventory(t, c); !got.Updated.Equal(v.Updated) {
		t.Fatal("restart replaced valid inventory")
	}
}

func TestCatalogInitializationVideoFailureDoesNotRetryCatalog(t *testing.T) {
	c := testConsole(t)
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20991029000000", Source: cacheproxy.UdonCatalogURL, Songs: []cacheproxy.CatalogSong{{ID: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(c.settings.VideoDir); err != nil {
		t.Fatal(err)
	}
	if !c.catalogReady(t.Context()) {
		t.Fatal("video availability blocked complete metadata")
	}
	v := waitInventory(t, c)
	if v.CoverageKnown || v.Error == "" || v.Catalog.Revision == "" {
		t.Fatal(v)
	}
	if _, err := os.Stat(c.settings.VideoDir); !os.IsNotExist(err) {
		t.Fatal("initialization recreated unavailable video directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.catalogReady(ctx) {
		t.Fatal("canceled initialization reported readiness")
	}
}

func TestCatalogInitializationFailureSchedulesRetryAfterConsent(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	var calls atomic.Int32
	c.settings.UpstreamMode = "proxy"
	c.client.Transport = catalogTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := c.StartUpstreamServices(); err != nil {
		t.Fatal(err)
	}
	if c.monitor != nil || calls.Load() != 0 {
		t.Fatal("initialization bypassed consent")
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	s := waitCatalogSync(t, c)
	if !s.Initializing || s.NextCheck.Sub(s.Finished) != 5*time.Second {
		t.Fatal("first failure did not schedule initialization retry", s.Initializing, s.NextCheck.Sub(s.Finished))
	}
	c.Close()
	c.mu.Lock()
	s = c.catalogSyncSnapshotLocked()
	c.mu.Unlock()
	if s.Initializing || !s.NextCheck.IsZero() {
		t.Fatal("shutdown left initialization active")
	}
}

func waitCatalogSync(t *testing.T, c *Console) catalogSyncStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		s := c.catalogSyncSnapshotLocked()
		c.mu.Unlock()
		if !s.Checking && !s.Finished.IsZero() {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("catalog sync did not finish")
	return catalogSyncStatus{}
}
