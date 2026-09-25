package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		filepath.Join(c.settings.StorageDir, "videos", "1-"+strings.Repeat("c", 64)+".mp4"): "video",
		filepath.Join(c.settings.StorageDir, "2", "metadata.json"):                          "{}",
		filepath.Join(c.settings.StorageDir, "3", "video.mp4"):                              "",
		filepath.Join(c.settings.StorageDir, "4", "video.mp4.part"):                         "partial",
		filepath.Join(c.settings.StorageDir, "other", "video.mp4"):                          "ignore",
		filepath.Join(c.settings.StorageDir, "videos", "2-"+strings.Repeat("a", 64)+".mp4"): "cache",
		filepath.Join(c.settings.StorageDir, strings.Repeat("b", 64)+".part"):               "partial",
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
