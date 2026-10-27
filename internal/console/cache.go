package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

func (c *Console) cacheAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", http.StatusForbidden)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	root, service, scanning, closing := libraryPaths(c.settings), c.service, c.batch.Running && c.batch.ScanOnly, c.closing
	c.mu.Unlock()
	if closing {
		http.Error(w, "程序正在退出", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result any
	var err error
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var input struct {
		StorageID string                      `json:"storageID"`
		Entries   []cacheproxy.CacheSelection `json:"entries"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&input)
	if err == nil && input.StorageID != cacheproxy.CacheStorageID(root) {
		err = errors.New("存储目录已切换，请刷新本地视频明细")
	}
	if err == nil {
		switch r.URL.Path {
		case "/api/cache/delete":
			if scanning {
				err = errors.New("扫描校验正在运行，本地视频受保护；请等待完成或停止扫描")
			} else if service != nil {
				result, err = service.DeleteCache(ctx, input.Entries)
			} else {
				var store *cacheproxy.Store
				store, err = c.businessStore()
				if err == nil {
					result, err = store.DeleteCache(ctx, root.VideoDir, input.Entries)
				}
			}
		case "/api/cache/open":
			var path string
			if len(input.Entries) == 0 {
				path, err = cacheproxy.CacheDirectory(root.VideoDir)
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
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, result)
}
