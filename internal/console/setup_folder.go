package console

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/desktop"
)

func (c *Console) selectSetupFolder(ctx context.Context, kind, initial string) (string, error) {
	title := map[string]string{"storage": "选择本地存储目录", "import": "选择已有视频文件夹", "vrcx": "选择 VRCX SQLite 数据库"}[kind]
	if title == "" || len(initial) > 4096 || strings.ContainsRune(initial, 0) || (initial != "" && !filepath.IsAbs(initial)) {
		return "", errors.New("无效的文件夹选择请求")
	}
	c.mu.Lock()
	if c.closing || !c.terms.valid() || c.setupFolderCancel != nil {
		c.mu.Unlock()
		return "", errors.New("请先同意条款，或关闭已打开的文件夹选择窗口")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	c.setupFolderCancel = cancel
	picker := c.setupFolderPicker
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.setupFolderCancel = nil
		c.mu.Unlock()
	}()
	if picker == nil {
		picker = desktop.SelectFolder
		if kind == "vrcx" {
			picker = desktop.SelectDatabase
		}
	}
	path, err := picker(ctx, title, initial)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || path == "" {
		return path, err
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", errors.New("所选文件夹无效，请重新选择")
	}
	if kind == "storage" {
		if err := c.validateStorageSelection(path, false); err != nil {
			return "", err
		}
	}
	return filepath.Clean(path), nil
}
