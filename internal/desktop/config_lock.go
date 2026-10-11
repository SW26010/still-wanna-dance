package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"still-wanna-dance/internal/apppaths"
)

type configLease struct {
	once    sync.Once
	release func() error
	err     error
}

func (l *configLease) Close() error { l.once.Do(func() { l.err = l.release() }); return l.err }

// LockConfig also protects standalone/custom-port consoles from concurrent writes.
func LockConfig(path string) (Lease, error) {
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
	return &configLease{release: release}, nil
}
