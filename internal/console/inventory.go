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
		if os.IsNotExist(err) {
			continue
		}
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
	c.mu.Lock()
	s := c.settings
	c.mu.Unlock()
	c.inventoryMu.Lock()
	defer c.inventoryMu.Unlock()
	if !c.inventory.Scanning && (s != c.inventorySettings || time.Since(c.inventory.Updated) >= 30*time.Second) {
		if s != c.inventorySettings {
			c.inventory = Inventory{}
		}
		c.inventorySettings = s
		c.inventory.Scanning = true
		go func() {
			result := scanInventory(s)
			c.inventoryMu.Lock()
			c.inventory = result
			c.inventoryMu.Unlock()
		}()
	}
	return c.inventory
}
