package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

func (c *Console) cacheAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	// Mutations must remain coordinated with storage switches and scans.
	// Read-only enumeration must not delay service lifecycle operations.
	if r.Method == "POST" {
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
	}
	c.mu.Lock()
	root, service, scanning, closing := c.settings.StorageDir, c.service, c.batch.Running && c.batch.ScanOnly, c.closing
	c.mu.Unlock()
	if closing {
		http.Error(w, "控制台正在退出", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result any
	var err error
	if r.Method == "GET" && r.URL.Path == "/api/cache" {
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
		}
		order := r.URL.Query().Get("sort")
		query := r.URL.Query().Get("q")
		// Match the search input's HTML maxlength: UTF-16 code units, not
		// UTF-8 bytes (or Unicode code points for supplementary characters).
		if err != nil || offset < 0 || offset > 10000000 || (order != "" && order != "recent" && order != "size" && order != "title") || !utf8.ValidString(query) || len(utf16.Encode([]rune(query))) > 300 {
			http.Error(w, "无效查询", 400)
			return
		}
		var page cacheproxy.CachePage
		page, err = c.cacheSnapshot(func(root string) (cacheproxy.CachePage, error) {
			return cacheproxy.ReadCachePage(ctx, root, query, order, offset)
		})
		result = page
	} else if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		var input struct {
			StorageID string                      `json:"storageID"`
			Entries   []cacheproxy.CacheSelection `json:"entries"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&input)
		if err == nil && input.StorageID != cacheproxy.CacheStorageID(root) {
			err = errors.New("存储目录已切换，请刷新缓存明细")
		}
		if err == nil {
			switch r.URL.Path {
			case "/api/cache/delete":
				if scanning {
					err = errors.New("扫描校验正在运行，缓存受保护；请等待完成或停止扫描")
				} else if service != nil {
					result, err = service.DeleteCache(ctx, input.Entries)
				} else {
					result, err = cacheproxy.DeleteCacheOffline(ctx, root, input.Entries)
				}
			case "/api/cache/open":
				var path string
				if len(input.Entries) == 0 {
					path, err = cacheproxy.CacheDirectory(root)
				} else if len(input.Entries) == 1 {
					path, err = cacheproxy.CacheFile(ctx, root, input.Entries[0])
				} else {
					err = errors.New("每次只能定位一个文件")
				}
				if err == nil {
					err = desktop.OpenCacheLocation(path, len(input.Entries) == 1)
				}
				result = map[string]bool{"ok": err == nil}
			default:
				http.NotFound(w, r)
				return
			}
		}
	} else {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, result)
}

// Only snapshot/validation use mu. The reader owns its database connection and
// never borrows engine lifetime, so Close need not wait for slow filesystem I/O.
func (c *Console) cacheSnapshot(read func(string) (cacheproxy.CachePage, error)) (cacheproxy.CachePage, error) {
	c.mu.Lock()
	root, revision, closing := c.settings.StorageDir, c.settingsRevision, c.closing
	c.mu.Unlock()
	if closing {
		return cacheproxy.CachePage{}, errors.New("控制台正在退出")
	}
	page, err := read(root)
	c.mu.Lock()
	changed := root != c.settings.StorageDir || revision != c.settingsRevision
	service, scanning, closing := c.service, c.batch.Running && c.batch.ScanOnly, c.closing
	c.mu.Unlock()
	if changed {
		return cacheproxy.CachePage{}, errors.New("存储设置已变化，请刷新缓存明细")
	}
	if closing {
		return cacheproxy.CachePage{}, errors.New("控制台正在退出")
	}
	if err != nil {
		return cacheproxy.CachePage{}, err
	}
	if service != nil {
		service.MarkCacheProtection(&page)
	}
	if scanning {
		for i := range page.Entries {
			page.Entries[i].Protected = true
		}
	}
	return page, nil
}
