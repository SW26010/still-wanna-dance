package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveStorage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DatabasePath string `json:"databasePath"`
		VideoDir     string `json:"videoDir"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "无效的数据位置请求", 400)
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	s, old, closing := c.savedSettings, c.savedSettings, c.closing
	activeDatabase := c.settings.DatabasePath
	c.mu.Unlock()
	if closing {
		http.Error(w, "程序正在退出", 409)
		return
	}
	s.DatabasePath, s.VideoDir = input.DatabasePath, input.VideoDir
	s, err := c.resolveSettings(s)
	if err == nil {
		switch r.URL.Path {
		case "/api/storage/check":
			var confirmation bool
			confirmation, err = c.inspectLibrarySelection(s, old)
			if err == nil {
				writeJSON(w, map[string]bool{"confirmVideoPolicy": confirmation})
				return
			}
		case "/api/storage/create-database", "/api/storage/prepare-database":
			if strings.TrimSpace(s.DatabasePath) == "" {
				err = errors.New("请选择要新建的数据库文件位置")
				break
			}
			if err = validateStorageLocation(s.DatabasePath, c.defaultDatabasePath(), c.portableDir()); err != nil {
				break
			}
			if r.URL.Path == "/api/storage/prepare-database" {
				if _, statErr := os.Lstat(s.DatabasePath); statErr == nil {
					// Existing files are inspected read-only, never recreated or migrated.
					if _, err = cacheproxy.LoadCatalogStatus(r.Context(), s.DatabasePath); err == nil {
						writeJSON(w, map[string]any{"path": s.DatabasePath, "created": false})
						return
					}
					break
				} else if !os.IsNotExist(statErr) {
					err = statErr
					break
				}
				if sameResourcePath(s.DatabasePath, old.DatabasePath) || sameResourcePath(s.DatabasePath, activeDatabase) {
					err = errors.New("已配置的数据库文件丢失，请恢复原文件，或选择新的文件名新建数据库")
					break
				}
			}
			var store *cacheproxy.Store
			store, err = cacheproxy.CreateStore(s.DatabasePath)
			if err == nil {
				err = store.Close()
			}
			if err == nil {
				writeJSON(w, map[string]any{"path": s.DatabasePath, "created": true})
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
	}
	http.Error(w, err.Error(), 400)
}
