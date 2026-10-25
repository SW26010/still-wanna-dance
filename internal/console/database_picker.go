package console

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/desktop"
)

func (c *Console) selectVRCXDatabase(ctx context.Context, initial string) (string, error) {
	title := "选择 VRCX SQLite 数据库"
	if len(initial) > 4096 || strings.ContainsRune(initial, 0) || (initial != "" && !filepath.IsAbs(initial)) {
		return "", errors.New("无效的数据库选择请求")
	}
	c.mu.Lock()
	if c.closing || !c.terms.valid() || c.databasePickerCancel != nil {
		c.mu.Unlock()
		return "", errors.New("请先同意条款，或关闭已打开的数据库选择窗口")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	c.databasePickerCancel = cancel
	picker := c.databasePicker
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.databasePickerCancel = nil
		c.mu.Unlock()
	}()
	if picker == nil {
		picker = desktop.SelectDatabase
	}
	path, err := picker(ctx, title, initial)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || path == "" {
		return path, err
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", errors.New("所选数据库无效，请重新选择")
	}
	return filepath.Clean(path), nil
}
