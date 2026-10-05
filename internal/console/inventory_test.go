package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestInventoryCoverageDoesNotRecreateMissingVideoDirectory(t *testing.T) {
	c := testConsole(t)
	defer c.Close()
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	cfg := fixtureCacheConfig()
	cfg.StorageDir = c.settings.StorageDir
	engine, err := cacheproxy.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StorageDir, "videos", strings.Repeat("a", 32)+".mp4")
	if err := os.WriteFile(path, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	before := waitInventory(t, c)
	if before.Error != "" || before.Videos != 1 {
		t.Fatalf("%+v", before)
	}
	snapshot, err := os.ReadFile(c.configPath + ".inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid store must fail before fetching coverage")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer api.Close()
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	c.startInventoryScan()
	after := c.localInventory()
	if after.Error == "" || after.Scanning {
		t.Fatalf("%+v", after)
	}
	after.Error = ""
	if after != before {
		t.Fatalf("previous inventory lost: before=%+v after=%+v", before, after)
	}
	if c.service != nil {
		t.Fatal("invalid store started engine")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("videos recreated: %v", err)
	}
	stored, err := os.ReadFile(c.configPath + ".inventory.json")
	if err != nil || string(stored) != string(snapshot) {
		t.Fatalf("saved snapshot changed: %v", err)
	}
}

func TestInventoryReadsWaitForExplicitScan(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	read := func() Inventory {
		t.Helper()
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+c.address+"/api/inventory", nil))
		var v Inventory
		if w.Code != http.StatusOK {
			t.Fatalf("inventory status: %d", w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for i := 0; i < 2; i++ {
		if v := read(); v != (Inventory{}) {
			t.Fatalf("read triggered inventory work: %+v", v)
		}
	}
	c.inventoryMu.Lock()
	started := c.inventoryDone != nil
	c.inventoryMu.Unlock()
	if started {
		t.Fatal("read started a scan")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/inventory/scan", nil)
	r.Header.Set("X-StepStash-Token", c.token)
	c.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("scan status: %d: %s", w.Code, w.Body.String())
	}
	v := waitInventory(t, c)
	if v.Updated.IsZero() || v.Error != "" || v.Scanning {
		t.Fatalf("explicit scan failed: %+v", v)
	}
	if got := read(); !got.Updated.Equal(v.Updated) || got.Scanning {
		t.Fatalf("read did not retain completed snapshot: %+v", got)
	}
}

func TestSavedEmptyStorageScansWithoutStartingEngine(t *testing.T) {
	c := testConsole(t)
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	v := waitInventory(t, c)
	if v.Error != "" || v.Videos != 0 || v.Bytes != 0 || v.Updated.IsZero() {
		t.Fatalf("%+v", v)
	}
	if c.service != nil {
		t.Fatal("inventory started engine")
	}
	entries, err := os.ReadDir(c.settings.StorageDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("scan initialized storage", entries, err)
	}
	// Initialized storage with a lost videos directory is not an empty new store.
	if err := os.WriteFile(filepath.Join(c.settings.StorageDir, "stepstash.sqlite"), []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	after := waitInventory(t, c)
	if after.Error == "" || !after.Updated.Equal(v.Updated) {
		t.Fatalf("missing videos overwrote prior result: %+v", after)
	}
}

func TestInventoryCountsPublishedFilesOnly(t *testing.T) {
	c := testConsole(t)
	for path, body := range map[string]string{
		filepath.Join(c.settings.StorageDir, "videos", strings.Repeat("c", 32)+".mp4"): "video",
		filepath.Join(c.settings.StorageDir, "2", "metadata.json"):                     "{}",
		filepath.Join(c.settings.StorageDir, "3", "video.mp4"):                         "",
		filepath.Join(c.settings.StorageDir, "4", "video.mp4.part"):                    "partial",
		filepath.Join(c.settings.StorageDir, "other", "video.mp4"):                     "ignore",
		filepath.Join(c.settings.StorageDir, "videos", strings.Repeat("a", 32)+".mp4"): "cache",
		filepath.Join(c.settings.StorageDir, strings.Repeat("b", 32)+".part"):          "partial",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v := scanInventory(context.Background(), c.settings)
	if v.Videos != 2 || v.Bytes != 10 || v.Error != "" {
		t.Fatalf("%+v", v)
	}
	if err := os.RemoveAll(c.settings.StorageDir); err != nil {
		t.Fatal(err)
	}
	v = scanInventory(context.Background(), c.settings)
	if v.Videos != 0 || v.Error == "" {
		t.Fatalf("%+v", v)
	}
}

func TestInventoryLifecycleCancelsCoverage(t *testing.T) {
	for _, action := range []string{"close", "change storage"} {
		t.Run(action, func(t *testing.T) {
			c := testConsole(t)
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			c.startInventoryScan()
			previous := waitInventory(t, c)
			before, err := os.ReadFile(c.configPath + ".inventory.json")
			if err != nil {
				t.Fatal(err)
			}
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			c.checksumURL = server.URL
			c.startInventoryScan()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("coverage request did not start")
			}
			c.inventoryMu.Lock()
			scanDone := c.inventoryDone
			c.inventoryMu.Unlock()
			settings := c.settings
			settings.StorageDir = t.TempDir()
			finished := make(chan error, 1)
			go func() {
				if action == "close" {
					finished <- c.Close()
				} else {
					finished <- c.save(settings)
				}
			}()
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("lifecycle action waited for coverage timeout")
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("coverage request was not canceled")
			}
			select {
			case <-scanDone:
			default:
				t.Fatal("lifecycle action left scan running")
			}
			after, err := os.ReadFile(c.configPath + ".inventory.json")
			if err != nil || string(after) != string(before) {
				t.Fatalf("cancellation changed persisted snapshot: %v", err)
			}
			if action == "close" {
				if got := c.localInventory(); got != previous {
					t.Fatalf("cancellation changed inventory: %+v", got)
				}
				c.startInventoryScan()
				if c.localInventory().Scanning {
					t.Fatal("scan started after close")
				}
			} else {
				if got := c.localInventory(); got != (Inventory{}) {
					t.Fatalf("old scan changed new library inventory: %+v", got)
				}
				c.checksumURL = ""
				c.startInventoryScan()
				if got := waitInventory(t, c); got.Error != "" || got.Updated.IsZero() {
					t.Fatalf("new library scan failed: %+v", got)
				}
			}
		})
	}
}

func TestInventoryReadDoesNotWaitForLifecycle(t *testing.T) {
	c := testConsole(t)
	c.lifecycleMu.Lock()
	done := make(chan Inventory, 1)
	go func() { done <- c.localInventory() }()
	select {
	case got := <-done:
		c.lifecycleMu.Unlock()
		if got.Catalog.Error != "" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		c.lifecycleMu.Unlock()
		<-done
		t.Fatal("inventory read waited for lifecycle lock")
	}
}

func TestInventoryReadDiscardsChangedSettings(t *testing.T) {
	for _, aba := range []bool{false, true} {
		t.Run(fmt.Sprint(aba), func(t *testing.T) {
			c := testConsole(t)
			original := c.settings
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan Inventory, 1)
			calls := 0
			go func() {
				done <- c.localInventoryWithLoader(func(ctx context.Context, root string) (cacheproxy.CatalogStatus, error) {
					calls++
					if calls == 1 {
						close(entered)
						<-release
						return cacheproxy.CatalogStatus{Revision: "stale", Error: "stale error"}, nil
					}
					return cacheproxy.CatalogStatus{Revision: "current"}, nil
				})
			}()
			<-entered
			next := original
			next.StorageDir = t.TempDir()
			if err := c.save(next); err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			if aba {
				if err := c.save(original); err != nil {
					close(release)
					<-done
					t.Fatal(err)
				}
			}
			close(release)
			got := <-done
			if calls != 2 || got.Catalog.Revision != "current" || got.Catalog.Error != "" {
				t.Fatal(calls, got)
			}
		})
	}
}

func TestInventoryReadBoundsSettingsRetries(t *testing.T) {
	c := testConsole(t)
	calls := 0
	got := c.localInventoryWithLoader(func(context.Context, string) (cacheproxy.CatalogStatus, error) {
		calls++
		c.mu.Lock()
		c.settingsRevision++
		c.mu.Unlock()
		return cacheproxy.CatalogStatus{Revision: "stale"}, nil
	})
	if calls != 2 || got.Catalog.Revision != "" || got.Catalog.Error == "" {
		t.Fatal(calls, got)
	}
}
