// Package apppaths defines the portable application's file ownership boundaries.
// Resolving paths never creates directories. Writers create only their parents.
package apppaths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const ConfigVersion = 3

type Paths struct {
	Config, Root, Lock, Terms, ChannelKey, Log, DefaultStorage string
}

func DefaultConfig(executable string) string {
	return filepath.Join(filepath.Dir(executable), "data", "config.json")
}

// ForConfig takes an absolute configuration filename. config.json owns its
// directory; any other filename owns a sibling <full-filename>.d directory.
// Relative user paths are always based on the configuration's parent directory.
func ForConfig(config string) Paths {
	base := filepath.Base(config)
	root := filepath.Dir(config)
	canonical := base == "config.json" || (runtime.GOOS == "windows" && strings.EqualFold(base, "config.json"))
	storage := "storage"
	if !canonical {
		root = config + ".d"
		storage = filepath.Join(base+".d", "storage")
	}
	return Paths{Config: config, Root: root, Lock: filepath.Join(root, "config.lock"),
		Terms: filepath.Join(root, "terms.json"), ChannelKey: filepath.Join(root, "channel.key"),
		Log: filepath.Join(root, "logs", "app.log"), DefaultStorage: storage}
}

func (p Paths) State(name string) string { return filepath.Join(p.Root, "state", name+".json") }

// Reserve canonical companions so a second configuration cannot overwrite them.
func ValidateConfigPath(config string) error {
	for _, name := range []string{"terms.json", "channel.key", "config.lock", "state", "logs", "storage"} {
		if strings.EqualFold(filepath.Base(config), name) {
			return fmt.Errorf("配置文件名 %s 为应用数据保留名称，请使用 config.json 或其他独立名称", name)
		}
	}
	return nil
}

// Old test layouts have no migration path. Reject known installation markers
// before the default launch creates data, locks or logs; never remove them.
func CheckLegacyInstall(appDir string) error {
	for _, name := range []string{"still-wanna-dance-console.json", "stepstash-console.json", "still-wanna-dance-data", "stepstash-data"} {
		if _, err := os.Lstat(filepath.Join(appDir, name)); err == nil {
			return fmt.Errorf("发现旧测试布局 %s；请保留原目录并在独立目录使用新版 SWD，不自动迁移", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("检查旧测试布局：%w", err)
		}
	}
	return nil
}
