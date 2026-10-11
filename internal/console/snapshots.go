package console

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
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
	// Persist absolute instants in UTC; never change upstream civil revisions.
	switch value := result.(type) {
	case Batch:
		value.Updated, value.Finished = value.Updated.UTC(), value.Finished.UTC()
		value.Catalog.CheckedAt = value.Catalog.CheckedAt.UTC()
		result = value
	case Inventory:
		value.Updated = value.Updated.UTC()
		value.Catalog.CheckedAt = value.Catalog.CheckedAt.UTC()
		result = value
	}
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
	path := c.files().State(kind)
	return workfiles.Write(path, data)
}

func readSnapshot[T any](c *Console, kind string) (result T, resultErr error) {
	defer func() {
		if resultErr != nil && !os.IsNotExist(resultErr) {
			slog.Warn("snapshot_load_failed", "path", c.files().State(kind), "error", resultErr)
		}
	}()
	var saved savedSnapshot[T]
	data, err := os.ReadFile(c.files().State(kind))
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
