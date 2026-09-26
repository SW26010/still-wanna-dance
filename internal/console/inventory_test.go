package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		filepath.Join(c.settings.StorageDir, "videos", strings.Repeat("c", 64)+".mp4"): "video",
		filepath.Join(c.settings.StorageDir, "2", "metadata.json"):                     "{}",
		filepath.Join(c.settings.StorageDir, "3", "video.mp4"):                         "",
		filepath.Join(c.settings.StorageDir, "4", "video.mp4.part"):                    "partial",
		filepath.Join(c.settings.StorageDir, "other", "video.mp4"):                     "ignore",
		filepath.Join(c.settings.StorageDir, "videos", strings.Repeat("a", 64)+".mp4"): "cache",
		filepath.Join(c.settings.StorageDir, strings.Repeat("b", 64)+".part"):          "partial",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v := scanInventory(c.settings)
	if v.Videos != 2 || v.Bytes != 10 || v.Error != "" {
		t.Fatalf("%+v", v)
	}
	if err := os.RemoveAll(c.settings.StorageDir); err != nil {
		t.Fatal(err)
	}
	v = scanInventory(c.settings)
	if v.Videos != 0 || v.Error == "" {
		t.Fatalf("%+v", v)
	}
}
