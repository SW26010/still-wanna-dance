package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/desktop"
)

func (c *Console) servePathPicker(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Purpose string `json:"purpose"`
		Path    string `json:"path"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "无效的路径选择请求", http.StatusBadRequest)
		return
	}
	path, err := c.selectPath(r.Context(), input.Purpose, input.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": path, "canceled": path == ""})
}

func (c *Console) selectPath(ctx context.Context, purpose, initial string) (string, error) {
	dialog := desktop.PathDialog{}
	switch purpose {
	case "database":
		dialog.Mode, dialog.Title = "database", "选择或新建 SWD 数据库"
	case "vrcx-database":
		dialog.Mode, dialog.Title = "open", "选择 VRCX SQLite 数据库"
	case "video-directory":
		dialog.Mode, dialog.Title = "folder", "选择完整视频目录"
	case "import-directory":
		dialog.Mode, dialog.Title = "folder", "选择视频导入目录"
	case "log-directory":
		dialog.Mode, dialog.Title = "folder", "选择 VRChat 日志目录"
	default:
		return "", errors.New("无效的路径选择用途")
	}
	initial = strings.TrimSpace(initial)
	if len(initial) > 4096 || strings.ContainsRune(initial, 0) {
		return "", errors.New("无效的路径选择请求")
	}
	if initial != "" && !filepath.IsAbs(initial) {
		initial = filepath.Join(filepath.Dir(c.configPath), initial)
	}
	dialog.Initial = initial
	c.mu.Lock()
	if c.closing || !c.terms.valid() || c.pathPickerCancel != nil {
		c.mu.Unlock()
		return "", errors.New("请先同意条款，或关闭已打开的路径选择窗口")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	c.pathPickerCancel = cancel
	picker := c.pathPicker
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.pathPickerCancel = nil
		c.mu.Unlock()
	}()
	if picker == nil {
		picker = desktop.SelectPath
	}
	path, err := picker(ctx, dialog)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || path == "" {
		return path, err
	}
	if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsRune(path, 0) {
		return "", errors.New("所选路径无效，请重新选择")
	}
	return filepath.Clean(path), nil
}
