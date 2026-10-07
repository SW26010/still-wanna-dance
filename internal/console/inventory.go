package console

import (
	"context"
	"fmt"
	"io"
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
	return c.localInventoryWithLoader(cacheproxy.LoadCatalogStatus)
}

func (c *Console) localInventoryWithLoader(load func(context.Context, string) (cacheproxy.CatalogStatus, error)) Inventory {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Active storage is immutable. Never borrow the engine or hold state locks
	// during database I/O; a saved configuration cannot invalidate this read.
	c.mu.Lock()
	root := c.settings.StorageDir
	c.mu.Unlock()
	status, err := load(ctx, root)
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

// Check before engine startup can create videos. Read at most one entry here;
// the actual inventory scan remains responsible for enumerating the files.
func checkInventoryDirectory(root string) error {
	dir := filepath.Join(root, "videos")
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		if entries, rootErr := os.ReadDir(root); rootErr == nil && len(entries) == 0 {
			return nil // A saved but never initialized empty store is valid.
		}
	}
	if err == nil {
		defer f.Close()
		_, err = f.ReadDir(1)
		if err == io.EOF {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("无法读取 %s：%w", dir, err)
	}
	return nil
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
	if c.checksumURL != "" {
		err := checkInventoryDirectory(s.StorageDir)
		if err == nil {
			err = c.ensureEngine()
		}
		if err != nil {
			c.mu.Unlock()
			c.inventoryMu.Lock()
			c.inventory.Error = err.Error()
			c.inventoryMu.Unlock()
			return
		}
	}
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
		if err := c.writeSnapshot("inventory", s.StorageDir, result); err != nil {
			c.inventory.Error = "无法保存扫描结果：" + err.Error()
			return
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
