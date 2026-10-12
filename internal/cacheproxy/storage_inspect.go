package cacheproxy

import (
	"fmt"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
)

// InspectStorage reuses the runtime's layout/schema checks without initializing
// a database, taking its writer lease, cleaning files, or reading video bodies.
// The runtime must still revalidate when opening; this is a point-in-time hint.
func InspectStorage(root string) (existing bool, err error) {
	if err := workfiles.CheckDir(root); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := checkStorageLayout(root); err != nil {
		return false, err
	}
	db, err := readStorageDatabase(root)
	if err != nil {
		return false, err
	}
	if db == nil {
		if _, err := os.Lstat(filepath.Join(root, "videos")); err == nil {
			return false, fmt.Errorf("媒体目录仍存在但存储数据库缺失；请恢复完整备份，原目录未修改")
		} else if !os.IsNotExist(err) {
			return false, err
		}
		return false, nil
	}
	defer db.Close()
	if _, err := CacheDirectory(root); err != nil {
		return true, err
	}
	return true, nil
}
