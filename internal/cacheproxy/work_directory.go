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
// Its location is supplied by the application, never by the video engine.
type WorkDirectory struct {
	path           string
	unlock         func() error
	mu             sync.Mutex
	cleanupPending bool
	closed         bool
	err            error
}

func OpenWorkDirectory(path string, log *slog.Logger) (*WorkDirectory, error) {
	return openWorkDirectory(path, log, true, true)
}

// OpenWorkDirectoryDeferred claims the workspace without deleting files until
// its separation from temporarily unavailable data resources can be verified.
func OpenWorkDirectoryDeferred(path string, log *slog.Logger) (*WorkDirectory, error) {
	return openWorkDirectory(path, log, true, false)
}

// CleanExistingWorkDirectory never creates an absent old workspace.
func CleanExistingWorkDirectory(path string, log *slog.Logger) error {
	work, err := openWorkDirectory(path, log, false, true)
	if err != nil {
		return err
	}
	return work.Close()
}

func openWorkDirectory(path string, log *slog.Logger, create, clean bool) (*WorkDirectory, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("工作目录不能为空")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if create {
		if err := workfiles.CheckFutureDir(path); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
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
	if clean {
		if _, err := cleanMediaWorkFiles(path, log, blockWork); err != nil {
			unlock()
			return nil, fmt.Errorf("清理临时视频块：%w", err)
		}
	}
	return &WorkDirectory{path: path, unlock: unlock, cleanupPending: !clean}, nil
}

func (w *WorkDirectory) Path() string { return w.path }

func (w *WorkDirectory) CleanupPending() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cleanupPending
}

// CompleteCleanup requires the owner to have rechecked data-path separation;
// no video engine or other writers may be using this workspace yet.
func (w *WorkDirectory) CompleteCleanup(log *slog.Logger) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	if !w.cleanupPending {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if _, err := cleanMediaWorkFiles(w.path, log, blockWork); err != nil {
		return fmt.Errorf("清理临时视频块：%w", err)
	}
	w.cleanupPending = false
	return nil
}

func (w *WorkDirectory) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		w.err = w.unlock()
	}
	return w.err
}
