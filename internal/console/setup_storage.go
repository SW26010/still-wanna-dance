package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

type setupCheck struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type setupStorage struct {
	Path       string     `json:"path"`
	Writable   setupCheck `json:"writable"`
	Library    setupCheck `json:"library"`
	Space      setupCheck `json:"space"`
	FreeBytes  uint64     `json:"freeBytes"`
	TotalBytes uint64     `json:"totalBytes"`
}

func (c *Console) defaultStorageDir() string {
	return filepath.Join(filepath.Dir(c.configPath), c.files().DefaultStorage)
}

func portableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// Keep custom media outside the installation, including aliases and paths whose
// last components do not exist yet. The default library remains inside Portable.
func validateStorageLocation(path, defaultPath, portable string, custom bool) error {
	if !custom && pathContains(path, defaultPath) && pathContains(defaultPath, path) {
		return nil
	}
	if portable == "" {
		return fmt.Errorf("无法确认 Portable 目录，请重试")
	}
	root, err := desktop.CanonicalPath(portable)
	if err != nil {
		return fmt.Errorf("无法确认 Portable 目录：%w", err)
	}
	target, err := desktop.CanonicalPath(path)
	if err != nil {
		return fmt.Errorf("无法确认存储目录：%w", err)
	}
	if pathContains(portable, path) || pathContains(root, target) {
		return fmt.Errorf("指定位置必须在 Portable 目录之外；使用内置位置请取消勾选“指定位置”")
	}
	return nil
}

func (c *Console) validateStorageSelection(path string, custom bool) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("请填写存储目录")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(c.configPath), path)
	}
	return validateStorageLocation(path, c.defaultStorageDir(), portableDir(), custom)
}

func (c *Console) inspectSetupStorage(path string) (setupStorage, error) {
	// Read-only/advisory inspection must not wait for an unrelated hosts UAC.
	if strings.TrimSpace(path) == "" {
		return setupStorage{}, fmt.Errorf("请填写存储目录")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(c.configPath), path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return setupStorage{}, err
	}
	result := setupStorage{Path: path, Writable: setupCheck{"ok", "目录可写；新目录会在首次使用时创建"}, Library: setupCheck{"ok", "新存储位置；不会迁移原数据库或媒体"}, Space: setupCheck{"unknown", "暂时无法获取剩余空间"}}
	if err := c.validateStorageSelection(path, false); err != nil {
		result.Writable = setupCheck{"failed", err.Error()}
	} else if err := writableDir(path); err != nil {
		result.Writable = setupCheck{"failed", err.Error()}
	}
	existing, err := cacheproxy.InspectStorage(path)
	if err != nil {
		result.Library = setupCheck{"failed", err.Error()}
	} else if existing {
		result.Library.Message = "已有当前格式存储库，将复用其视频与统计"
	}
	// Lost active stores must not be advertised as safe empty targets.
	if path == c.settings.StorageDir && !existing && err == nil && c.storageUsageKnown() {
		result.Library = setupCheck{"failed", "当前已使用的存储库失联或数据库缺失，请恢复原目录与数据库"}
	}
	ancestor := path
	for {
		if info, err := os.Stat(ancestor); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return result, nil
		}
		ancestor = parent
	}
	if free, total, err := setupDiskSpace(ancestor); err == nil {
		result.FreeBytes = free
		result.TotalBytes = total
		result.Space = setupCheck{"ok", fmt.Sprintf("分区总容量 %.2f GiB · 可用 %.2f GiB", float64(total)/(1<<30), float64(free)/(1<<30))}
	} else {
		result.Space.Message = "无法获取剩余空间：" + err.Error()
	}
	return result, nil
}
