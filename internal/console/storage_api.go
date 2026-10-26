package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
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
	if r.URL.Path == "/api/storage/select-database" {
		initial := strings.TrimSpace(input.DatabasePath)
		if initial != "" && !filepath.IsAbs(initial) {
			initial = filepath.Join(filepath.Dir(c.configPath), initial)
		}
		path, err := c.selectDatabase(r.Context(), "打开 SWD 数据库", initial)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"path": path, "canceled": path == ""})
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	s, old, closing := c.savedSettings, c.savedSettings, c.closing
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
		case "/api/storage/create-database":
			if strings.TrimSpace(s.DatabasePath) == "" {
				err = errors.New("请填写要新建的数据库文件位置")
				break
			}
			if err = validateStorageLocation(s.DatabasePath, c.defaultDatabasePath(), c.portableDir()); err != nil {
				break
			}
			var store *cacheproxy.Store
			store, err = cacheproxy.CreateStore(s.DatabasePath)
			if err == nil {
				err = store.Close()
			}
			if err == nil {
				writeJSON(w, map[string]string{"path": s.DatabasePath})
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
	}
	http.Error(w, err.Error(), 400)
}
