package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotContractIndependentOfSettings(t *testing.T) {
	c := testConsole(t)
	if err := c.writeSnapshot("test", libraryPaths(c.settings), 42); err != nil {
		t.Fatal(err)
	}
	path := c.files().State("test")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || string(fields["schemaVersion"]) != "2" {
		t.Fatal(string(data))
	}
	var saved savedSnapshot[int]
	if err := json.Unmarshal(data, &saved); err != nil || saved.DatabasePath != filepath.Join("library", "storage.sqlite") || saved.VideoDir != filepath.Join("library", "videos") {
		t.Fatal("snapshot paths are not portable and independently scoped", string(data), err)
	}
	// Neither current network values nor obsolete settings in a snapshot need
	// validation to display the result for the same library.
	c.settings.UpstreamMode = "future-mode"
	fields["settings"] = json.RawMessage(`{"upstreamMode":"obsolete"}`)
	data, _ = json.Marshal(fields)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 42 {
		t.Fatal(got, err)
	}
	for _, field := range []string{"databasePath", "videoDir"} {
		original := fields[field]
		fields[field] = json.RawMessage(`"other-resource"`)
		data, _ = json.Marshal(fields)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := readSnapshot[int](c, "test"); err != nil || got != 0 {
			t.Fatal("snapshot crossed a changed resource", field, got, err)
		}
		fields[field] = original
	}
	for _, version := range []string{"0", "1", "999"} {
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
	c, err := newConsoleWithWorkForTest(t, filepath.Join(old, "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selected := c.settings
	selected.DatabasePath, selected.VideoDir = c.defaultDatabasePath(), c.defaultVideoDir()
	if err := c.save(selected); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = newConsoleWithWorkForTest(t, c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeSnapshot("test", libraryPaths(c.settings), 42); err != nil {
		t.Fatal(err)
	}
	c.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	c, err = newConsoleWithWorkForTest(t, filepath.Join(moved, "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 42 {
		t.Fatal(got, err)
	}
	setTestLibraryPaths(&c.settings, filepath.Join(base, "external"))
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 0 {
		t.Fatal("reused different library", got, err)
	}
	if err := c.writeSnapshot("test", libraryPaths(c.settings), 43); err != nil {
		t.Fatal(err)
	}
	if got, err := readSnapshot[int](c, "test"); err != nil || got != 43 {
		t.Fatal(got, err)
	}
}
