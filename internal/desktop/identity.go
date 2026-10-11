package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CanonicalPath resolves aliases in existing ancestors without creating a new
// configuration or its parent. The first launch may have neither yet.
func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := canonicalExistingPath(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	resolved, err = CanonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(abs)), nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, err := CanonicalPath(a)
	if err != nil {
		return false
	}
	b, err = CanonicalPath(b)
	if err != nil {
		return false
	}
	if a == b {
		return true
	}
	// SameFile also recognizes hard links, short names and filesystem aliases.
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	if ae == nil && be == nil {
		return os.SameFile(ai, bi)
	}
	return runtime.GOOS == "windows" && os.IsNotExist(ae) && os.IsNotExist(be) && strings.EqualFold(a, b)
}

func NewIdentity(config string) (Identity, error) {
	exe, started, err := processIdentity(uint32(os.Getpid()))
	if err != nil {
		return Identity{}, err
	}
	exe, err = CanonicalPath(exe)
	if err != nil {
		return Identity{}, err
	}
	config, err = CanonicalPath(config)
	if err != nil {
		return Identity{}, err
	}
	id := AppIdentity
	id.Executable, id.Config = exe, config
	id.PID, id.Started = uint32(os.Getpid()), started
	return id, nil
}

func (id Identity) valid() bool {
	return id.App == AppIdentity.App && id.Protocol == AppIdentity.Protocol &&
		id.PID != 0 && id.Started != "" && filepath.IsAbs(id.Executable) && filepath.IsAbs(id.Config)
}

func (id Identity) sameInstallation(other Identity) bool {
	return samePath(id.Executable, other.Executable) && samePath(id.Config, other.Config)
}

func (id Identity) conflict() error {
	return fmt.Errorf("已有 Still Wanna Dance 正在运行（PID %d）。\n程序：%s\n配置：%s\n本次请求的程序或配置不同，请先自行退出已有实例后再启动。", id.PID, id.Executable, id.Config)
}
