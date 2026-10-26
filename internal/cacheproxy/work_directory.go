package cacheproxy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"still-wanna-dance/internal/workfiles"
)

// WorkDirectory owns disposable block files for one application instance.
// Open it before the file logger/database; close it after all engines stop.
// Its location is supplied by apppaths, never by an editable user setting.
type WorkDirectory struct {
	path   string
	unlock func() error
	once   sync.Once
	err    error
}

func OpenWorkDirectory(path string, log *slog.Logger) (*WorkDirectory, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("工作目录不能为空")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := workfiles.CheckFutureDir(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	if err := workfiles.CheckDir(path); err != nil {
		return nil, err
	}
	lease := filepath.Join(path, ".lock")
	if info, err := os.Lstat(lease); err == nil && !workfiles.Regular(info) {
		return nil, fmt.Errorf("工作目录锁不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	unlock, err := lockDirectory(lease)
	if err != nil {
		return nil, fmt.Errorf("工作目录正被其他实例使用：%w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	if _, err := cleanMediaWorkFiles(path, log, blockWork); err != nil {
		unlock()
		return nil, fmt.Errorf("清理临时视频块：%w", err)
	}
	return &WorkDirectory{path: path, unlock: unlock}, nil
}

func (w *WorkDirectory) Path() string { return w.path }

func (w *WorkDirectory) Close() error {
	w.once.Do(func() { w.err = w.unlock() })
	return w.err
}
