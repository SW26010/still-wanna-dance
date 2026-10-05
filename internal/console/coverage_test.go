package console

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestCoverageRecomputesFromDatabaseWhenRemoteFails(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700); err != nil {
		t.Fatal(err)
	}
	// a is shared by two songs, b is an old version, c is missing, d truncated.
	for _, ch := range []string{"a", "b", "c", "d"} {
		key := strings.Repeat(ch, 32)
		if ch != "c" {
			body := "test"
			if ch == "d" {
				body = "x"
			}
			if err := os.WriteFile(filepath.Join(c.settings.StorageDir, "videos", key+".mp4"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var calls atomic.Int32
	var fail atomic.Bool
	var timeout atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if timeout.Load() {
			<-r.Context().Done()
			return
		}
		if fail.Load() {
			http.Error(w, "offline", 503)
			return
		}
		fmt.Fprintf(w, `{"code":200,"data":{"time":"20261001000000","groups":[{"entries":[{"id":1,"checksum":%q},{"id":2,"checksum":%q,"disablePublic":true},{"id":3,"checksum":%q},{"id":4,"checksum":%q},{"id":5,"checksum":%q}]}]}}`, strings.Repeat("a", 32), strings.Repeat("a", 32), strings.Repeat("e", 32), strings.Repeat("c", 32), strings.Repeat("d", 32))
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	c.startInventoryScan()
	v := waitInventory(t, c)
	if v.Error != "" || !v.CoverageKnown || v.TotalSongs != 5 || v.CoveredSongs != 3 || calls.Load() != 1 {
		t.Fatalf("%+v calls=%d", v, calls.Load())
	}
	// A restart must retain the coverage, and a failed refresh must not turn it into zero.
	reopened, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.localInventory(); got.CoveredSongs != 3 || !got.CoverageKnown {
		t.Fatalf("lost snapshot: %+v", got)
	}
	fail.Store(true)
	if err := os.Remove(filepath.Join(c.settings.StorageDir, "videos", strings.Repeat("a", 32)+".mp4")); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	after := waitInventory(t, c)
	if after.Error != "" || after.CoveredSongs != 1 || !after.Updated.After(v.Updated) || after.Catalog.Error == "" || !after.Catalog.CheckedAt.Equal(v.Catalog.CheckedAt) {
		t.Fatalf("did not recompute local coverage: %+v", after)
	}
	timeout.Store(true)
	c.client.Timeout = 20 * time.Millisecond
	c.startInventoryScan()
	after = waitInventory(t, c)
	if after.Error != "" || after.CoveredSongs != 1 || after.Catalog.Error == "" || !after.Catalog.CheckedAt.Equal(v.Catalog.CheckedAt) {
		t.Fatal(after)
	}
}

func TestCoverageOnlineStillUsesDatabaseSnapshot(t *testing.T) {
	c := testConsole(t)
	cfg := cacheproxy.DefaultConfig()
	cfg.StorageDir = c.settings.StorageDir
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.service = engine
	key := strings.Repeat("a", 32)
	if err := syncTestCatalog(c, engine, context.Background(), map[string]string{"1": key, "2": key}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StorageDir, "videos", key+".mp4"), []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":200,"data":{"time":"20261001000000","groups":[{"entries":[{"id":1,"checksum":%q}]}]}}`, key)
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	c.startInventoryScan()
	v := waitInventory(t, c)
	// ID 2 was retained locally. Counting the HTTP response directly gives 1.
	if v.Error != "" || v.TotalSongs != 2 || v.CoveredSongs != 2 || v.CatalogRevision != "20261001000000" || v.Catalog.CheckedAt.IsZero() {
		t.Fatal(v)
	}
}
