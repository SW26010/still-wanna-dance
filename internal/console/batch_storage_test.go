package console

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestBatchScanRejectsMissingUsedStorageAfterRestart(t *testing.T) {
	for _, evidence := range []string{"inventory", "failed attempt"} {
		t.Run(evidence, func(t *testing.T) {
			c := testConsole(t)
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			cfg := fixtureCacheConfig()
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, libraryPaths(c.settings).VideoDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			video := filepath.Join("videos", strings.Repeat("a", 32)+".mp4")
			if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.VideoDir), video), []byte("video"), 0600); err != nil {
				t.Fatal(err)
			}
			kind := "inventory"
			if evidence == "inventory" {
				c.startInventoryScan()
				if got := waitInventory(t, c); got.Error != "" || got.Videos != 1 {
					t.Fatal(got)
				}
			} else {
				kind = "batch-last-attempt"
				if err := c.writeSnapshot(kind, fixtureLibraryPaths(filepath.Dir(cfg.VideoDir)), Batch{Finished: time.Now(), Phase: "catalog unavailable"}); err != nil {
					t.Fatal(err)
				}
			}
			snapshotPath := c.files().State(kind)
			snapshot, err := os.ReadFile(snapshotPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Dir(cfg.VideoDir) + "-unavailable"
			if err := os.Rename(filepath.Dir(cfg.VideoDir), moved); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.service != nil || c.lastBatch.Total != 0 {
				t.Fatal("test requires no engine or successful batch")
			}
			beforeInventory, beforeBatch := c.inventory, c.batch
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "unexpected request", http.StatusServiceUnavailable)
			}))
			defer upstream.Close()
			c.checksumURL, c.client.Transport = upstream.URL, http.DefaultTransport
			err = c.startBatchMode(true)
			if err == nil {
				_, statErr := os.Stat(filepath.Join(filepath.Dir(cfg.VideoDir), "storage.sqlite"))
				t.Fatalf("missing used storage accepted: videos=%d lastBatch=%d recreatedDatabase=%t", c.inventory.Videos, c.lastBatch.Total, statErr == nil)
			}
			if !strings.Contains(err.Error(), filepath.Dir(cfg.VideoDir)) || !strings.Contains(err.Error(), "数据库不可用") {
				t.Fatal("missing actionable diagnostic", err)
			}
			if c.service != nil || c.batchDone != nil || requests.Load() != 0 {
				t.Fatal("rejection started media work")
			}
			if !reflect.DeepEqual(c.inventory, beforeInventory) || !reflect.DeepEqual(c.batch, beforeBatch) {
				t.Fatal("rejection changed restored state")
			}
			if _, err := os.Stat(filepath.Dir(cfg.VideoDir)); !os.IsNotExist(err) {
				t.Fatal("missing storage root recreated", err)
			}
			if got, err := os.ReadFile(snapshotPath); err != nil || !bytes.Equal(got, snapshot) {
				t.Fatal("saved evidence changed", err)
			}
			if got, err := os.ReadFile(filepath.Join(moved, video)); err != nil || string(got) != "video" {
				t.Fatal("moved media changed", err)
			}
		})
	}
}

func TestBatchScanNeverInitializesMissingDatabaseFromEmptyInventory(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	if got := waitInventory(t, c); got.Error != "" || got.Videos != 0 || got.Updated.IsZero() {
		t.Fatal(got)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(testStorageRoot(c.settings), testStorageRoot(c.settings)+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if err := c.startBatchMode(true); err == nil {
		t.Fatal("empty inventory authorized database initialization")
	}
	if c.service != nil {
		t.Fatal("missing database started an engine")
	}
	if _, err := os.Stat(testStorageRoot(c.settings)); !os.IsNotExist(err) {
		t.Fatal("missing storage recreated", err)
	}
}
