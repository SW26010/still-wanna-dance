package console

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/desktop"
)

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
func validateStorageLocation(path, defaultPath, portable string) error {
	if pathContains(path, defaultPath) && pathContains(defaultPath, path) {
		return nil
	}
	if portable == "" {
		return fmt.Errorf("无法确认程序文件夹，请重试")
	}
	root, err := desktop.CanonicalPath(portable)
	if err != nil {
		return fmt.Errorf("无法确认程序文件夹：%w", err)
	}
	target, err := desktop.CanonicalPath(path)
	if err != nil {
		return fmt.Errorf("无法确认存储目录：%w", err)
	}
	if pathContains(portable, path) || pathContains(root, target) {
		return fmt.Errorf("请选择程序文件夹之外的位置，或填写默认存储目录")
	}
	return nil
}

func (c *Console) validateStorageSelection(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("请填写存储目录")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(c.configPath), path)
	}
	return validateStorageLocation(path, c.defaultStorageDir(), portableDir())
}
