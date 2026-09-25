package console

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Inventory counts files, not verified checksums or unique songs across stores.
type Inventory struct {
	Library  int       `json:"library"`
	Cache    int       `json:"cache"`
	Bytes    int64     `json:"bytes"`
	Scanning bool      `json:"scanning"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error"`
}

var libraryID = regexp.MustCompile(`^[1-9][0-9]*$`)
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
	for _, dir := range []struct {
		path    string
		library bool
	}{{s.SongsDir, true}, {s.CacheDir, false}} {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("无法读取 %s：%v", dir.path, err))
			continue
		}
		for _, entry := range entries {
			if dir.library && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && libraryID.MatchString(entry.Name()) {
				add(filepath.Join(dir.path, entry.Name(), "video.mp4"), &result.Library)
			} else if !dir.library && cacheName.MatchString(entry.Name()) {
				add(filepath.Join(dir.path, entry.Name()), &result.Cache)
			}
		}
	}
	result.Updated = time.Now()
	result.Error = strings.Join(problems, "；")
	return result
}

func (c *Console) localInventory() Inventory {
	c.inventoryMu.Lock()
	first := c.inventory.Updated.IsZero() && c.inventory.Error == "" && !c.inventory.Scanning
	c.inventoryMu.Unlock()
	if first {
		c.startInventoryScan()
	}
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
