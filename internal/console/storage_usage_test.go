package console

import (
	"bytes"
	"context"
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
	"still-wanna-dance/internal/upstreamstate"
)

func TestStorageEntryPointsRejectLostLibrary(t *testing.T) {
	for _, layout := range []string{"missing", "empty"} {
		for _, entry := range []string{"gateway", "monitor_catalog", "monitor_playback", "batch_scan", "inventory", "inventory_without_coverage"} {
			t.Run(layout+"/"+entry, func(t *testing.T) {
				c := testConsole(t)
				if err := c.save(c.settings); err != nil {
					t.Fatal(err)
				}
				root := testStorageRoot(c.settings)
				cfg := fixtureCacheConfig()
				cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, fixtureLibraryPaths(root).VideoDir
				engine, err := cacheproxy.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Close(); err != nil {
					t.Fatal(err)
				}
				video := filepath.Join("videos", strings.Repeat("a", 32)+".mp4")
				if err := os.WriteFile(filepath.Join(root, video), []byte("video"), 0600); err != nil {
					t.Fatal(err)
				}
				c.startInventoryScan()
				if got := waitInventory(t, c); got.Error != "" || got.Videos != 1 {
					t.Fatal("inventory did not record library use", got)
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
				database, err := os.ReadFile(filepath.Join(root, "storage.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := os.ReadFile(c.files().State("inventory"))
				if err != nil {
					t.Fatal(err)
				}
				moved := root + "-unavailable"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if layout == "empty" {
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
				}
				c = restartTestConsole(t, c)
				beforeInventory, beforeBatch := c.inventory, c.batch
				var requests atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					http.Error(w, "unexpected request", http.StatusServiceUnavailable)
				}))
				defer upstream.Close()
				c.checksumURL, c.client.Transport = upstream.URL, http.DefaultTransport
				switch entry {
				case "gateway":
					err = c.start()
				case "monitor_catalog":
					c.syncCatalogs(context.Background(), []upstreamstate.CatalogResponse{monitorFull("kiva", "20261001000000", "song", strings.Repeat("a", 32))})
				case "monitor_playback":
					c.syncPlaybackURL(context.Background(), upstreamstate.PlaybackURLResponse{
						SongID: 1, URL: "https://play.udon.dance/files/1/1-test.mp4?e=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&s=1",
						API: "https://api.udon.dance/Api/Songs/play", Node: "cf", QueryStartedAt: time.Now().Add(-time.Second), ObservedAt: time.Now(),
					})
				case "batch_scan":
					err = c.startBatchMode(true)
				default:
					if entry == "inventory_without_coverage" {
						c.checksumURL = ""
					}
					c.startInventoryScan()
					if got := waitInventory(t, c); got.Error == "" {
						t.Errorf("missing lost-library diagnostic: %+v", got)
					}
				}
				if (entry == "gateway" || entry == "batch_scan") && err == nil {
					t.Errorf("missing lost-library diagnostic: %v", err)
				}
				if c.service != nil {
					t.Fatal("lost library started a replacement engine")
				}
				if entries, err := os.ReadDir(root); (layout == "missing" && !os.IsNotExist(err)) || (layout == "empty" && (err != nil || len(entries) != 0)) {
					t.Fatal("rejection changed the target directory", entries, err)
				}
				gotInventory := c.inventory
				gotInventory.Error = beforeInventory.Error
				if !reflect.DeepEqual(gotInventory, beforeInventory) || !reflect.DeepEqual(c.batch, beforeBatch) || requests.Load() != 0 {
					t.Fatal("rejection changed inventory/tasks or requested upstream data")
				}
				if got, err := os.ReadFile(c.files().State("inventory")); err != nil || !bytes.Equal(got, snapshot) {
					t.Fatal("saved inventory changed", err)
				}
				if got, err := os.ReadFile(filepath.Join(moved, "storage.sqlite")); err != nil || !bytes.Equal(got, database) {
					t.Fatal("original database changed", err)
				}
				if got, err := os.ReadFile(filepath.Join(moved, video)); err != nil || string(got) != "video" {
					t.Fatal("original media changed", err)
				}
				if entry == "gateway" {
					if layout == "empty" {
						if err := os.Remove(root); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Rename(moved, root); err != nil {
						t.Fatal(err)
					}
					c = restartTestConsole(t, c)
					if err := c.start(); err != nil {
						t.Fatal("restored library could not reopen", err)
					}
				}
			})
		}
	}
}
