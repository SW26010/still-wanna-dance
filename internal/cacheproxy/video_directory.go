package cacheproxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

// Video ownership is independent of database ownership and directory naming.
func lockVideoDirectory(path string) (func() error, error) {
	path, err := CacheDirectory(path)
	if err != nil {
		return nil, err
	}
	if err := checkVideoLayout(path); err != nil {
		return nil, err
	}
	// Preserve known old data without opening or migrating it.
	if strings.EqualFold(filepath.Base(path), "videos") {
		if err := checkDatabaseDirectory(filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	lease := filepath.Join(path, ".swd.lock")
	info, err := os.Lstat(lease)
	if err == nil && !workfiles.Regular(info) {
		return nil, errors.New("视频目录锁不是普通文件")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	unlock, err := lockDirectory(lease)
	if err != nil {
		return nil, fmt.Errorf("视频目录正被其他实例使用：%w", err)
	}
	return unlock, nil
}

// Only the disposable child may be created at engine startup. A missing video
// root must remain missing so losing a disk cannot silently create an empty one.
func preparePublicationDirectory(videoDir, stage string) error {
	if _, err := CacheDirectory(videoDir); err != nil {
		return err
	}
	if err := os.Mkdir(stage, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return workfiles.CheckDir(stage)
}
