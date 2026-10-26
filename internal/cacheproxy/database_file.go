package cacheproxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
)

// Database aliases must not create separate writer leases or SQLite companion
// namespaces for one physical file. This restriction does not apply to videos.
func validateDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !workfiles.Regular(info) {
		return errors.New("存储数据库不是普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, actual) {
		return errors.New("数据库文件在检查期间发生变化，请重新选择")
	}
	links, err := fileLinkCount(f)
	if err != nil {
		return fmt.Errorf("无法确认数据库文件的硬链接数量：%w", err)
	}
	if links != 1 {
		return errors.New("数据库文件存在硬链接别名或名称已失效，不能安全打开；请正常退出后使用独立副本及必要恢复文件")
	}
	// EvalSymlinks also expands Windows short filenames. Keep the selected
	// pathname as the resource identity; ask for its real spelling instead of
	// silently changing that identity or opening a second companion namespace.
	return validateDatabasePathSpelling(path)
}

func validateDatabasePathSpelling(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if pathIdentity(canonical) != pathIdentity(path) {
		return fmt.Errorf("数据库位置包含路径别名，请使用实际完整路径：%s", canonical)
	}
	return nil
}
