package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"still-wanna-dance/internal/apppaths"
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
	return samePath(id.Executable, other.Executable) && sameConfigPath(id.Config, other.Config)
}

// The configuration's canonical pathname determines its data root and the
// base for relative settings. Distinct hard links do not share that identity
// (and an atomic settings save can separate their contents later).
func sameConfigPath(a, b string) bool {
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
	// Preserve Windows case aliases without conflating distinct files in a
	// case-sensitive directory. samePath also handles not-yet-created configs.
	return runtime.GOOS == "windows" && strings.EqualFold(a, b) && samePath(a, b)
}

// InstanceConflictError rejects a different installation without starting a
// recovery service or changing the existing owner.
type InstanceConflictError struct {
	Existing Identity
}

const instanceConflictInstruction = "已有 SWD 正在运行"
const instanceConflictExplanation = "本次启动已取消。请先退出正在运行的 SWD，再重新启动。"

func (e *InstanceConflictError) Error() string {
	return instanceConflictInstruction + "。" + instanceConflictExplanation + "\n" + e.details()
}

func (e *InstanceConflictError) details() string {
	id := e.Existing
	details := fmt.Sprintf("程序：%s", id.Executable)
	if !sameConfigPath(id.Config, apppaths.DefaultConfig(id.Executable)) {
		details += "\n配置：" + id.Config
	}
	return fmt.Sprintf("%s\nPID：%d", details, id.PID)
}

func (id Identity) conflict() error {
	return &InstanceConflictError{Existing: id}
}
