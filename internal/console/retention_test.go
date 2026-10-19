package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVideoStorageModesPersistAndApplyOnRestart(t *testing.T) {
	c := testConsole(t)
	if c.settings.VideoStorageMode != "none" || c.settings.MaxCacheBytes != 0 {
		t.Fatal("default must not retain unprotected videos")
	}
	for _, mode := range []string{"none", "limited", "unlimited"} {
		s := c.settings
		s.VideoStorageMode, s.MaxCacheBytes = mode, 5<<30
		if err := c.save(s); err != nil {
			t.Fatal(err)
		}
		if c.settings.VideoStorageMode != "none" || c.settings.MaxCacheBytes != 0 {
			t.Fatal("saving changed active settings")
		}
		_, fields, err := readConfigFile(c.configPath)
		if err != nil || string(fields["videoStorageMode"]) != `"`+mode+`"` || fields["cacheUnlimited"] != nil {
			t.Fatalf("choice was not stored explicitly: %s %v", fields["videoStorageMode"], err)
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.settings.VideoStorageMode != mode || loaded.settings.MaxCacheBytes != 5<<30 {
			t.Fatalf("capacity choice lost: %+v", loaded.settings)
		}
		initializeTestLibrary(t, loaded)
		path := filepath.Join(loaded.settings.StorageDir, "videos", strings.Repeat("a", 32)+".mp4")
		if err := os.WriteFile(path, []byte("retained video"), 0600); err != nil {
			t.Fatal(err)
		}
		loaded.mu.Lock()
		err = loaded.ensureEngine()
		loaded.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); mode == "none" && !os.IsNotExist(err) || mode != "none" && err != nil {
			t.Fatalf("mode %s did not apply its runtime budget: %v", mode, err)
		}
		if err := loaded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheLimitSettingsPersist(t *testing.T) {
	c := testConsole(t)
	settings := c.settings
	settings.MaxCacheBytes = 5 << 30
	settings.VideoStorageMode = "limited"
	if err := c.save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.MaxCacheBytes != settings.MaxCacheBytes {
		t.Fatal("limit lost on restart")
	}
	for _, tc := range []struct {
		mode   string
		budget int64
	}{
		{"limited", -1},
		{"limited", 0},
		{"", 5 << 30},
		{"other", 5 << 30},
	} {
		invalid := settings
		invalid.VideoStorageMode, invalid.MaxCacheBytes = tc.mode, tc.budget
		if err := c.save(invalid); err == nil {
			t.Fatal("invalid video storage choice accepted")
		}
	}
	if c.savedSettings.MaxCacheBytes != 5<<30 {
		t.Fatal("invalid setting changed saved limit")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureEngine(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRetentionSettings(t *testing.T) {
	c := testConsole(t)
	if c.settings.RequestRetentionDays != 30 {
		t.Fatal("missing default retention")
	}
	for _, days := range []int{7, 0} {
		settings := c.settings
		settings.RequestRetentionDays = days
		if err := c.save(settings); err != nil {
			t.Fatal(err)
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.settings.RequestRetentionDays != days {
			t.Fatal("retention lost on restart")
		}
		loaded.Close()
	}
	for _, days := range []int{-1, 36501} {
		settings := c.settings
		settings.RequestRetentionDays = days
		if err := c.save(settings); err == nil {
			t.Fatal("invalid retention accepted")
		}
	}
}
