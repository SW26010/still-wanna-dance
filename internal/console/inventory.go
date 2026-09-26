package console

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Inventory counts canonical video files; integrity is checked on use.
type Inventory struct {
	Videos   int       `json:"videos"`
	Bytes    int64     `json:"bytes"`
	Scanning bool      `json:"scanning"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error"`
}

var cacheName = regexp.MustCompile(`^[0-9a-f]{64}\.mp4$`)

func scanInventory(s Settings) Inventory {
	var result Inventory
	var problems []string
	add := func(path string, count *int) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			problems = append(problems, err.Error())
			return
		}
		if info.Mode().IsRegular() && info.Size() > 0 {
			*count++
			result.Bytes += info.Size()
		}
	}
	dir := filepath.Join(s.StorageDir, "videos")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		// Saving a fresh storage root does not start the engine or create videos.
		// Only an existing, empty root is uninitialized; lost/unreadable stores
		// and initialized stores missing videos must still preserve prior results.
		if rootEntries, rootErr := os.ReadDir(s.StorageDir); rootErr == nil && len(rootEntries) == 0 {
			err = nil
		}
	}
	if err != nil {
		problems = append(problems, fmt.Sprintf("无法读取 %s：%v", dir, err))
	} else {
		for _, entry := range entries {
			if cacheName.MatchString(entry.Name()) {
				add(filepath.Join(dir, entry.Name()), &result.Videos)
			}
		}
	}
	result.Updated = time.Now()
	result.Error = strings.Join(problems, "；")
	return result
}

func (c *Console) localInventory() Inventory {
	c.inventoryMu.Lock()
	defer c.inventoryMu.Unlock()
	return c.inventory
}

func (c *Console) startInventoryScan() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return
	}
	s := c.settings
	c.inventoryMu.Lock()
	defer c.inventoryMu.Unlock()
	if c.inventory.Scanning {
		return
	}
	c.inventory.Scanning = true
	c.inventory.Error = ""
	c.inventoryGeneration++
	generation := c.inventoryGeneration
	done := make(chan struct{})
	c.inventoryDone = done
	go func() {
		defer close(done)
		result := scanInventory(s)
		c.inventoryMu.Lock()
		defer c.inventoryMu.Unlock()
		if generation != c.inventoryGeneration {
			return
		}
		c.inventory.Scanning = false
		if result.Error != "" {
			c.inventory.Error = result.Error
			return
		}
		if err := c.writeSnapshot("inventory", s, result); err != nil {
			c.inventory.Error = "无法保存扫描结果：" + err.Error()
			return
		}
		c.inventory = result
	}()
}
