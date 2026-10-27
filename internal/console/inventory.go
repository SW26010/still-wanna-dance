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

// Inventory counts immutable MD5 files without hashing video contents.
type Inventory struct {
	Catalog         cacheproxy.CatalogStatus `json:"catalog"`
	CoverageKnown   bool                     `json:"coverageKnown"`
	CoveredSongs    int                      `json:"coveredSongs"`
	TotalSongs      int                      `json:"totalSongs"`
	CatalogRevision string                   `json:"catalogRevision"`
	Videos          int                      `json:"videos"`
	Bytes           int64                    `json:"bytes"`
	Scanning        bool                     `json:"scanning"`
	Updated         time.Time                `json:"updated"`
	Error           string                   `json:"error"`
}

var cacheName = regexp.MustCompile(`^[0-9a-f]{32}\.mp4$`)

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
		if info.Mode().IsRegular() {
			*count++
			result.Bytes += info.Size()
		}
	}
	dir := s.VideoDir
	entries, err := os.ReadDir(dir)
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
	return c.localInventoryWithLoader(cacheproxy.LoadCatalogStatus)
}

func (c *Console) localInventoryWithLoader(load func(context.Context, string) (cacheproxy.CatalogStatus, error)) Inventory {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Active storage is immutable. Never borrow the engine or hold state locks
	// during database I/O; a saved configuration cannot invalidate this read.
	c.mu.Lock()
	database := c.settings.DatabasePath
	c.mu.Unlock()
	status, err := load(ctx, database)
	c.inventoryMu.Lock()
	c.mu.Lock()
	v := c.inventory
	c.mu.Unlock()
	c.inventoryMu.Unlock()
	v.Catalog = status
	if err != nil {
		v.Catalog = cacheproxy.CatalogStatus{Error: "无法读取本地清单状态：" + err.Error()}
	}
	return v
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
	if c.checksumURL != "" {
		_, err := cacheproxy.CacheDirectory(libraryPaths(s).VideoDir)
		if err == nil {
			_, err = c.ensureEngineLocked()
		}
		if err != nil {
			c.inventoryMu.Lock()
			c.inventory.Error = err.Error()
			c.inventoryMu.Unlock()
			return
		}
	}
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
			c.addInventoryCoverage(ctx, &result)
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
		if err := c.writeSnapshot("inventory", libraryPaths(s), result); err != nil {
			result.Error = "本次扫描已完成，但无法保存结果；退出后将丢失本次结果：" + err.Error()
		}
		c.inventory = result
	}()
}

func (c *Console) addInventoryCoverage(ctx context.Context, result *Inventory) {
	c.mu.Lock()
	engine := c.service
	c.mu.Unlock()
	if engine == nil {
		result.Error = "本地清单数据库尚未打开"
		return
	}
	refreshErr := c.refreshLocalCatalog(ctx, engine)
	catalog, release, err := engine.ReadLocalCatalog(ctx)
	defer release()
	result.Catalog = catalog.Status
	if err != nil {
		result.Error = "无法读取本地资源：" + err.Error()
		return
	}
	if catalog.Status.Revision == "" || len(catalog.Songs) == 0 {
		result.Error = "本地尚无有效 MD5 清单，无法计算覆盖率"
		if refreshErr != nil {
			result.Error += "：" + refreshErr.Error()
		}
		return
	}
	for _, song := range catalog.Songs {
		checksum := song.MD5
		if catalog.Files.Present[checksum] {
			result.CoveredSongs++
		}
	}
	result.TotalSongs = len(catalog.Songs)
	result.CoverageKnown = true
	result.CatalogRevision = catalog.Status.Revision
	result.Updated = time.Now()
}
