package console

import (
	"net/http"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

func (c *Console) serveImportSpace(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if len(source) > 4096 || strings.ContainsRune(source, 0) || !filepath.IsAbs(source) {
		http.Error(w, "无效的导入来源目录", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	videoDir := c.settings.VideoDir
	// A restored batch belongs to its original destination, even before the
	// user restores the active storage settings required to resume it.
	if c.importStatus.Present && source == c.importStatus.Source {
		videoDir = c.importStatus.VideoDir
	}
	c.mu.Unlock()
	writeJSON(w, map[string]string{"mode": cacheproxy.EstimateImportSpace(source, videoDir), "videoDir": videoDir})
}
