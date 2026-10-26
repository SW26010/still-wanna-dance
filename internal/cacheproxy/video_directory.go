package cacheproxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

// Video ownership is independent of database ownership. Keep the legacy parent
// lease for a directory named videos so an old writer cannot bypass the new
// lease, including when this engine uses a different database.
func lockVideoDirectory(path string, store *Store) (func() error, error) {
	path, err := CacheDirectory(path)
	if err != nil {
		return nil, err
	}
	if err := checkVideoLayout(path); err != nil {
		return nil, err
	}
	leasePaths := []string{filepath.Join(path, ".swd.lock")}
	if strings.EqualFold(filepath.Base(path), "videos") {
		if err := checkDatabaseDirectory(filepath.Dir(path)); err != nil {
			return nil, err
		}
		legacy := filepath.Join(filepath.Dir(path), ".lock")
		if store == nil || pathIdentity(legacy) != pathIdentity(databaseLeasePath(store.path)) {
			leasePaths = append(leasePaths, legacy)
		}
	}
	var unlocks []func() error
	release := func() error {
		var err error
		for i := len(unlocks) - 1; i >= 0; i-- {
			err = errors.Join(err, unlocks[i]())
		}
		return err
	}
	for _, lease := range leasePaths {
		info, err := os.Lstat(lease)
		if err == nil && !workfiles.Regular(info) {
			err = errors.New("视频目录锁不是普通文件")
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, errors.Join(err, release())
		}
		unlock, err := lockDirectory(lease)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("视频目录正被其他实例使用：%w", err), release())
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
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
