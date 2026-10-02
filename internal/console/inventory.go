package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

// Inventory counts canonical video files; integrity is checked on use.
type Inventory struct {
	CoverageKnown   bool      `json:"coverageKnown"`
	CoveredSongs    int       `json:"coveredSongs"`
	TotalSongs      int       `json:"totalSongs"`
	CatalogRevision string    `json:"catalogRevision"`
	Videos          int       `json:"videos"`
	Bytes           int64     `json:"bytes"`
	Scanning        bool      `json:"scanning"`
	Updated         time.Time `json:"updated"`
	Error           string    `json:"error"`
}

var cacheName = regexp.MustCompile(`^[0-9a-f]{64}\.mp4$`)

func scanInventory(ctx context.Context, s Settings) Inventory {
	var result Inventory
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}
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
			if err := ctx.Err(); err != nil {
				problems = append(problems, err.Error())
				break
			}
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
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return
	}
	s := c.settings
	c.mu.Unlock()
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
	ctx, cancel := context.WithCancel(context.Background())
	c.inventoryCancel = cancel
	go func() {
		defer close(done)
		defer cancel()
		result := scanInventory(ctx, s)
		if result.Error == "" && c.checksumURL != "" {
			c.addInventoryCoverage(ctx, s, &result)
		}
		c.inventoryMu.Lock()
		defer c.inventoryMu.Unlock()
		if generation != c.inventoryGeneration {
			return
		}
		c.inventory.Scanning = false
		c.inventoryCancel = nil
		if ctx.Err() != nil {
			return // Lifecycle cancellation preserves the last successful snapshot.
		}
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

func (c *Console) addInventoryCoverage(ctx context.Context, s Settings, result *Inventory) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	checksums, revision, err := c.fetchCatalogChecksums(ctx)
	if err != nil {
		result.Error = "无法获取曲目覆盖率：" + err.Error()
		return
	}
	cached, err := cacheproxy.CachedChecksums(ctx, s.StorageDir)
	if err != nil {
		result.Error = "无法读取本地版本：" + err.Error()
		return
	}
	for _, checksum := range checksums {
		if checksum == "" {
			result.Error = "清单包含无效校验和或同 ID 校验和冲突，覆盖率未更新"
			return
		}
		if cached[checksum] {
			result.CoveredSongs++
		}
	}
	result.TotalSongs = len(checksums)
	result.CoverageKnown = true
	result.CatalogRevision = revision
	result.Updated = time.Now()
}
