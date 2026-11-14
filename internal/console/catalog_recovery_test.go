package console

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmptyCatalogRefreshRecoversAfterEOF(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			c := testConsole(t)
			var healthy atomic.Bool
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !healthy.Load() {
					if truncated {
						w.Header().Set("Content-Length", "1000")
						fmt.Fprint(w, `{"code":200,`)
					}
					return
				}
				fmt.Fprint(w, `{"code":200,"data":{"time":"20261115000000","groups":[{"entries":[{"id":42,"checksum":"0123456789abcdef0123456789abcdef"}]}]}}`)
			}))
			defer origin.Close()
			c.checksumURL, c.client.Transport = origin.URL, http.DefaultTransport
			c.startInventoryScan()
			failed := waitInventory(t, c)
			wantCause := "清单格式无效"
			if truncated {
				wantCause = "unexpected EOF"
			}
			if failed.CoverageKnown || failed.Error == "" || !strings.Contains(c.metadata.CatalogError(), wantCause) {
				t.Fatalf("empty catalog failure was not exposed: %+v", failed)
			}
			healthy.Store(true)
			c.startInventoryScan()
			recovered := waitInventory(t, c)
			if !recovered.CoverageKnown || recovered.TotalSongs != 1 || recovered.Error != "" || c.metadata.CatalogError() != "" {
				t.Fatalf("manual retry did not recover: %+v", recovered)
			}
			c = restartTestConsole(t, c)
			got := c.localInventory()
			if !got.CoverageKnown || got.TotalSongs != 1 || got.Error != "" || got.Catalog.Revision != recovered.CatalogRevision {
				t.Fatalf("restart lost recovered catalog: %+v", got)
			}
		})
	}
}

// Uses the production resolver and direct clients, but only isolated storage.
// No gateway, hosts changes, video downloads or background monitor are started.
func TestLiveEmptyCatalogRefreshAndReopen(t *testing.T) {
	if os.Getenv("SWD_LIVE_CATALOG") != "1" {
		t.Skip("opt-in live catalog request")
	}
	fixture := testConsole(t)
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := newConsoleWithWorkForTest(t, fixture.configPath, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if c.settings.UpstreamMode != "direct" || c.localInventory().Catalog.Revision != "" {
		t.Fatal("live check requires a fresh catalog and direct mode")
	}
	started := time.Now()
	c.startInventoryScan()
	c.inventoryMu.Lock()
	done := c.inventoryDone
	c.inventoryMu.Unlock()
	select {
	case <-done:
	case <-time.After(100 * time.Second):
		t.Fatal("live catalog refresh stalled")
	}
	v := c.localInventory()
	if !v.CoverageKnown || v.TotalSongs == 0 || v.Error != "" {
		t.Fatalf("live empty catalog refresh failed: %+v", v)
	}
	if c.service != nil || c.monitor != nil {
		t.Fatal("catalog check started unrelated services")
	}
	t.Logf("direct catalog refresh: songs=%d revision=%s elapsed=%s", v.TotalSongs, v.Catalog.Revision, time.Since(started))
	c = restartTestConsole(t, c)
	got := c.localInventory()
	if !got.CoverageKnown || got.TotalSongs != v.TotalSongs || got.Catalog.Revision != v.Catalog.Revision || got.Error != "" {
		t.Fatalf("reopen lost live catalog: songs=%d revision=%s error=%s", got.TotalSongs, got.Catalog.Revision, got.Error)
	}
	t.Log("reopened database and inventory match the successful live refresh")
}
