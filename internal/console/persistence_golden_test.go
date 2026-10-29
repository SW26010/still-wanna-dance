package console

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigurationAndInventoryGolden(t *testing.T) {
	root := t.TempDir()
	b, err := os.ReadFile("testdata/config-v8.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := newConsoleWithWorkForTest(t, path, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.settings.RequestRetentionMode != "unlimited" {
		t.Fatal("explicit unlimited retention was lost")
	}
	if c.settings.DownloadConcurrencySoftLimit != 32 || c.settings.RestoreHostsOnStopOrExit || c.settings.VRChatLogDir != filepath.Join(root, "logs") {
		t.Fatal("explicit settings were lost", c.settings)
	}
	if c.settings.PrepareAheadSeconds != 500 || c.settings.RequestRetentionDays != 0 || c.settings.AutoAccelerate || testStorageRoot(c.settings) != filepath.Join(root, "storage") || c.settings.VideoStorageMode != "none" || c.settings.VideoBudgetBytes != 0 {
		t.Fatal("configuration defaults changed")
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	_, fields, err := readConfigFile(path)
	if err != nil || !strings.Contains(string(fields["futurePreference"]), "9007199254740993") {
		t.Fatal("unknown integer changed", err)
	}
	b, err = os.ReadFile("testdata/inventory-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(c.files().State("inventory")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.files().State("inventory"), b, 0600); err != nil {
		t.Fatal(err)
	}
	v, err := readSnapshot[Inventory](c, "inventory")
	if err != nil || v.Updated.UnixNano() != 1791716220123456789 || v.Catalog.Revision != "20261011105700" || v.TotalSongs != 3 || v.UnresolvedSongs != 1 {
		t.Fatal(v, err)
	}
	v.Updated = v.Updated.In(time.FixedZone("east", 8*3600))
	if err := c.writeSnapshot("inventory", libraryPaths(c.settings), v); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(c.files().State("inventory"))
	if err != nil || strings.Contains(string(b), "+08:00") || !strings.Contains(string(b), "123456789Z") {
		t.Fatal(string(b), err)
	}
}

func TestConfigurationRejectsFrozenOldFormats(t *testing.T) {
	for _, name := range []string{"config-v5.json", "config-v6.json", "config-v7.json"} {
		t.Run(name, func(t *testing.T) {
			original, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			c := testConsole(t)
			if err := os.WriteFile(c.configPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if other, err := newConsoleWithWorkForTest(t, c.configPath, c.address); err == nil {
				other.Close()
				t.Fatal("opened an unsupported configuration")
			}
			if err := c.save(c.settings); err == nil {
				t.Fatal("overwrote an unsupported configuration")
			}
			after, err := os.ReadFile(c.configPath)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("unsupported configuration changed", err)
			}
		})
	}
}
