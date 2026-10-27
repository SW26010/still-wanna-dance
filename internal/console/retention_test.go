package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVideoStorageModesPersistAndApplyOnRestart(t *testing.T) {
	for _, mode := range []string{"none", "limited", "unlimited"} {
		c := testConsole(t)
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
		loaded := restartTestConsole(t, c)
		if loaded.settings.VideoStorageMode != mode || loaded.settings.MaxCacheBytes != 5<<30 {
			t.Fatalf("capacity choice lost: %+v", loaded.settings)
		}
		initializeTestLibrary(t, loaded)
		path := filepath.Join(testStorageRoot(loaded.settings), "videos", strings.Repeat("a", 32)+".mp4")
		if err := os.WriteFile(path, []byte("retained video"), 0600); err != nil {
			t.Fatal(err)
		}
		loaded.lifecycleMu.Lock()
		_, err = loaded.ensureEngineLocked()
		loaded.lifecycleMu.Unlock()
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
	loaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
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
	if _, err := c.ensureEngineLocked(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRetentionSettings(t *testing.T) {
	c := testConsole(t)
	if c.settings.RequestRetentionMode != "unlimited" || c.settings.RequestRetentionDays != 0 {
		t.Fatal("missing default retention")
	}
	for _, mode := range []string{"none", "limited", "unlimited"} {
		settings := c.settings
		settings.RequestRetentionMode, settings.RequestRetentionDays = mode, 7
		if err := c.save(settings); err != nil {
			t.Fatal(err)
		}
		if c.settings.RequestRetentionMode != "unlimited" || c.settings.RequestRetentionDays != 0 {
			t.Fatal("saving changed active request retention")
		}
		loaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.settings.RequestRetentionMode != mode || loaded.settings.RequestRetentionDays != 7 {
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
	for _, mode := range []string{"", "other", "limited"} {
		settings := c.settings
		settings.RequestRetentionMode = mode
		if err := c.save(settings); err == nil {
			t.Fatal("invalid mode or zero-day limited retention accepted")
		}
	}
}

func TestRequestRetentionDefaultsAndExplicitModes(t *testing.T) {
	for _, tc := range []struct {
		fields, mode string
		days         int
	}{
		{``, "unlimited", 0},
		{`,"requestRetentionDays":null`, "unlimited", 0},
		{`,"requestRetentionMode":null,"requestRetentionDays":null`, "unlimited", 0},
		{`,"requestRetentionMode":"limited","requestRetentionDays":7`, "limited", 7},
		{`,"requestRetentionMode":"none","requestRetentionDays":0`, "none", 0},
		{`,"requestRetentionMode":"unlimited","requestRetentionDays":7`, "unlimited", 7},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(config, []byte(fmt.Sprintf(`{"schemaVersion":7%s}`, tc.fields)), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:18081")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.settings.RequestRetentionMode != tc.mode || c.settings.RequestRetentionDays != tc.days {
				t.Fatal("unexpected retention mode or days", c.settings.RequestRetentionMode, c.settings.RequestRetentionDays)
			}
			if err := c.save(c.settings); err != nil {
				t.Fatal(err)
			}
			_, fields, err := readConfigFile(config)
			if err != nil || string(fields["requestRetentionMode"]) != `"`+tc.mode+`"` {
				t.Fatal("mode was not saved explicitly", fields["requestRetentionMode"], err)
			}
		})
	}
}
