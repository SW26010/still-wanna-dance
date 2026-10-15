package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigurationAndInventoryGolden(t *testing.T) {
	root := t.TempDir()
	b, err := os.ReadFile("testdata/config-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(path, "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.settings.PrepareAheadSeconds != 500 || c.settings.RequestRetentionDays != 0 || c.settings.AutoStartCDN || c.settings.StorageDir != filepath.Join(root, "storage") {
		t.Fatal("configuration defaults changed")
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
	_, fields, err := readConfigFile(path)
	if err != nil || !strings.Contains(string(fields["futurePreference"]), "9007199254740993") {
		t.Fatal("unknown integer changed", err)
	}
	b, err = os.ReadFile("testdata/inventory-v1.json")
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
	if err != nil || v.Updated.UnixNano() != 1791716220123456789 || v.Catalog.Revision != "20261011105700" {
		t.Fatal(v, err)
	}
	v.Updated = v.Updated.In(time.FixedZone("east", 8*3600))
	if err := c.writeSnapshot("inventory", c.settings.StorageDir, v); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(c.files().State("inventory"))
	if err != nil || strings.Contains(string(b), "+08:00") || !strings.Contains(string(b), "123456789Z") {
		t.Fatal(string(b), err)
	}
}
