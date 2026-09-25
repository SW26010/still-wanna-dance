package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type configLease struct {
	once    sync.Once
	release func() error
	err     error
}

func (l *configLease) Close() error { l.once.Do(func() { l.err = l.release() }); return l.err }

// LockConfig also protects standalone/custom-port consoles from concurrent writes.
func LockConfig(path string) (Lease, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	release, err := lockConfigFile(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("已有实例正在使用配置 %s：%w", path, err)
	}
	return &configLease{release: release}, nil
}
