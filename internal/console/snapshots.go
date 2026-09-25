package console

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type savedSnapshot[T any] struct {
	Settings Settings `json:"settings"`
	Result   T        `json:"result"`
}

func sameLibrary(a, b Settings) bool {
	return a.SongsDir == b.SongsDir && a.CacheDir == b.CacheDir
}

// Replace atomically, so interruption or a failed write leaves the last good file.
func (c *Console) writeSnapshot(kind string, settings Settings, result any) error {
	data, err := json.MarshalIndent(savedSnapshot[any]{c.storedSettings(settings), result}, "", "  ")
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
	settings, err := c.resolveSettings(saved.Settings)
	if err != nil {
		return saved.Result, err
	}
	if !sameLibrary(settings, c.settings) {
		var empty T
		return empty, nil
	}
	return saved.Result, nil
}

// Called before publication by New, or while c.mu is held after changing roots.
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
