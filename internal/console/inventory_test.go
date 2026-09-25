package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryCountsPublishedFilesOnly(t *testing.T) {
	c := testConsole(t)
	for path, body := range map[string]string{
		filepath.Join(c.settings.SongsDir, "1", "video.mp4"):                "video",
		filepath.Join(c.settings.SongsDir, "2", "metadata.json"):            "{}",
		filepath.Join(c.settings.SongsDir, "3", "video.mp4"):                "",
		filepath.Join(c.settings.SongsDir, "4", "video.mp4.part"):           "partial",
		filepath.Join(c.settings.SongsDir, "other", "video.mp4"):            "ignore",
		filepath.Join(c.settings.CacheDir, strings.Repeat("a", 64)+".mp4"):  "cache",
		filepath.Join(c.settings.CacheDir, strings.Repeat("b", 64)+".part"): "partial",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v := scanInventory(c.settings)
	if v.Library != 1 || v.Cache != 1 || v.Bytes != 10 || v.Error != "" {
		t.Fatalf("%+v", v)
	}
	if err := os.RemoveAll(c.settings.CacheDir); err != nil {
		t.Fatal(err)
	}
	v = scanInventory(c.settings)
	if v.Library != 1 || v.Cache != 0 || v.Error == "" {
		t.Fatalf("%+v", v)
	}
}
