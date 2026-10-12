package desktop

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	for _, path := range []string{p.Config, p.Terms, p.State("inventory"), p.State("batch"), p.State("attempt"), p.State("throughput"), p.State("setup"), p.State("setup-apply")} {
		if err := cleanConfigWorkFile(path); err != nil {
			return err
		}
	}
	// Only SHA-256 receipt slots belong to this writer. Never traverse unknown
	// files/directories or remove published receipts used for idempotent replay.
	dir := filepath.Dir(p.State("setup-apply"))
	if err := workfiles.CheckDir(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if setupReceiptWorkName.MatchString(entry.Name()) {
			destination := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "."), ".swd-tmp")
			if err := cleanConfigWorkFile(filepath.Join(dir, destination)); err != nil {
				return err
			}
		}
	}
	return nil
}

var setupReceiptWorkName = regexp.MustCompile(`^\.setup-apply-[0-9a-f]{64}\.json\.swd-tmp$`)

func cleanConfigWorkFile(path string) error {
	removed, err := workfiles.Clean(path)
	if err != nil {
		slog.Error("config_work_cleanup_failed", "path", path, "error", err)
		return fmt.Errorf("清理配置工作文件 %s：%w", path, err)
	}
	if removed {
		slog.Info("config_work_cleanup_removed", "path", path)
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
