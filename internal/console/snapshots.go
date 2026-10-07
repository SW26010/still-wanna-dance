package console

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const snapshotSchemaVersion = 1

type savedSnapshot[T any] struct {
	SchemaVersion int    `json:"schemaVersion"`
	StorageDir    string `json:"storageDir"`
	Result        T      `json:"result"`
}

func sameLibrary(a, b Settings) bool {
	return a.StorageDir == b.StorageDir
}

// Replace atomically, so interruption or a failed write leaves the last good file.
func (c *Console) writeSnapshot(kind, storageDir string, result any) error {
	// Store only the library origin. Network settings and credentials have no
	// bearing on whether an inventory or task result belongs to this library.
	base := filepath.Dir(c.configPath)
	if pathContains(base, storageDir) {
		if rel, err := filepath.Rel(base, storageDir); err == nil {
			storageDir = rel
		}
	}
	data, err := json.MarshalIndent(savedSnapshot[any]{snapshotSchemaVersion, storageDir, result}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.configPath), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.configPath+"."+kind+".json")
}

func readSnapshot[T any](c *Console, kind string) (T, error) {
	var saved savedSnapshot[T]
	data, err := os.ReadFile(c.configPath + "." + kind + ".json")
	if err != nil {
		return saved.Result, err
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		return saved.Result, err
	}
	if saved.SchemaVersion != snapshotSchemaVersion || saved.StorageDir == "" {
		var empty T
		return empty, fmt.Errorf("不支持的快照格式")
	}
	root := saved.StorageDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(c.configPath), root)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		var empty T
		return empty, err
	}
	if root != c.settings.StorageDir {
		var empty T
		return empty, nil
	}
	return saved.Result, nil
}

// Load into an unpublished Console. Callers publish the resulting snapshots under state locks.
func (c *Console) loadSnapshots() {
	c.lastBatch = Batch{}
	if saved, err := readSnapshot[Batch](c, "batch"); err == nil {
		saved.Running = false
		c.lastBatch = saved
	}
	c.batch = c.lastBatch
	if saved, err := readSnapshot[Batch](c, "attempt"); err == nil && !saved.Finished.IsZero() {
		saved.Running = false
		c.batch = saved
	}
	c.inventoryMu.Lock()
	defer c.inventoryMu.Unlock()
	c.inventorySettings = c.settings
	c.inventoryGeneration++
	c.inventory = Inventory{}
	if saved, err := readSnapshot[Inventory](c, "inventory"); err == nil {
		saved.Scanning = false
		c.inventory = saved
	}
}
