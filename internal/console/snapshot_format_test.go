package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotContractIndependentOfSettings(t *testing.T) {
	c := testConsole(t)
	if err := c.writeSnapshot("test", c.settings.StorageDir, 42); err != nil {
		t.Fatal(err)
	}
	path := c.configPath + ".test.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || string(fields["schemaVersion"]) != "1" || string(fields["storageDir"]) != `"data"` {
		t.Fatal(string(data))
	}
	// Neither current network values nor obsolete settings in a snapshot need
	// validation to display the result for the same library.
	c.settings.DownloadUpstream = "future-node"
	fields["settings"] = json.RawMessage(`{"upstreamMode":"obsolete"}`)
	data, _ = json.Marshal(fields)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 42 {
		t.Fatal(got, err)
	}
	for _, version := range []string{"0", "999"} {
		fields["schemaVersion"] = json.RawMessage(version)
		data, _ = json.Marshal(fields)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := readSnapshot[int](c, "test"); err == nil || got != 0 {
			t.Fatal(got, err)
		}
	}
}

func TestSnapshotFollowsPortableMoveAndSeparatesLibraries(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "old")
	c, err := New(filepath.Join(old, "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeSnapshot("test", c.settings.StorageDir, 42); err != nil {
		t.Fatal(err)
	}
	c.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	c, err = New(filepath.Join(moved, "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 42 {
		t.Fatal(got, err)
	}
	c.settings.StorageDir = filepath.Join(base, "external")
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 0 {
		t.Fatal("reused different library", got, err)
	}
	if err := c.writeSnapshot("test", c.settings.StorageDir, 43); err != nil {
		t.Fatal(err)
	}
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 43 {
		t.Fatal(got, err)
	}
}
