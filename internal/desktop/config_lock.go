package desktop

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/workfiles"
)

type configLease struct {
	path    string
	mu      sync.Mutex
	closed  bool
	once    sync.Once
	release func() error
	err     error
}

func (l *configLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.once.Do(func() { l.closed = true; l.err = l.release() })
	return l.err
}

// CleanWorkFiles is an explicit startup step, after logging is installed and
// before constructing writers. It cannot run without a live configuration lease.
func (l *configLease) CleanWorkFiles() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return fmt.Errorf("configuration lease is closed")
	}
	p := apppaths.ForConfig(l.path)
	if err := workfiles.CheckDir(p.Root); err != nil {
		return err
	}
	for _, path := range []string{p.Config, p.Terms, p.State("inventory"), p.State("batch-last-success"), p.State("batch-last-attempt"), p.State("download-routes"), p.State("video-import"), p.VideoImport, p.State("webui-port")} {
		if removed, err := workfiles.Clean(path); err != nil {
			// The atomic writer will still reject a blocked slot when a save needs it.
			slog.Warn("已跳过配置工作文件清理", "event_name", "config_work_cleanup_failed", "path", path, "error", err)
		} else if removed {
			slog.Info("配置工作文件已清理", "event_name", "config_work_cleanup_removed", "path", path)
		}
	}
	return nil
}

// LockConfig also protects standalone/custom-port consoles from concurrent writes.
func LockConfig(path string) (*configLease, error) {
	path, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	if err := apppaths.ValidateConfigPath(path); err != nil {
		return nil, err
	}
	lockPath := apppaths.ForConfig(path).Lock
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, err
	}
	release, err := lockConfigFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("已有实例正在使用配置 %s：%w", path, err)
	}
	return &configLease{path: path, release: release}, nil
}
