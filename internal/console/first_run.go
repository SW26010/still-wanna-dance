package console

import (
	"fmt"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

// A new desktop installation prepares its shared business storage. Only an
// unconfigured instance may create its default library; opening a configured
// database remains strict, including when that database has disappeared.
// Existing default data needs an explicit choice because a lost configuration
// also loses its video retention policy. No extra bootstrap marker is needed.
func (c *Console) prepareDefaultLibrary() error {
	s := c.settings
	s.DatabasePath, s.VideoDir = c.defaultDatabasePath(), c.defaultVideoDir()
	if err := c.validateResourcePaths(s); err != nil {
		return err
	}
	if err := checkUnconfiguredLibrary(s.DatabasePath, s.VideoDir); err != nil {
		return err
	}
	if err := checkPreviousLibrary(c.configPath, c.identity.Executable); err != nil {
		return err
	}
	if err := os.MkdirAll(s.VideoDir, 0700); err != nil {
		return err
	}
	if err := workfiles.CheckDir(s.VideoDir); err != nil {
		return err
	}
	if err := workfiles.Probe(s.VideoDir); err != nil {
		return err
	}
	store, err := cacheproxy.CreateStore(s.DatabasePath)
	if err != nil {
		return err
	}
	c.store, c.settings = store, s
	return nil
}

// Check only known previous positions, without reading old config or adopting
// data. This runs only before automatic initialization, never on ordinary open.
func checkPreviousLibrary(config, executable string) error {
	if sameResourcePath(config, apppaths.DefaultConfig(executable)) {
		previousRoot := filepath.Join(filepath.Dir(executable), "data")
		previousConfig := filepath.Join(previousRoot, "config.json")
		if _, err := os.Lstat(previousConfig); err == nil {
			return fmt.Errorf("原默认位置已有配置，请保留原文件并在存储设置中选择已有数据库和视频保留方式：%s", previousConfig)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, root := range previousLibraryRoots(config, executable) {
		if err := checkUnconfiguredLibrary(filepath.Join(root, "storage", "storage.sqlite"), filepath.Join(root, "storage", "videos")); err != nil {
			return err
		}
	}
	return nil
}

// Detection and explicit selection use the same bounded set of old positions.
func previousLibraryRoots(config, executable string) []string {
	roots := []string{apppaths.ForConfig(config).Root}
	if sameResourcePath(config, apppaths.DefaultConfig(executable)) {
		roots = append(roots, filepath.Join(filepath.Dir(executable), "data"))
	}
	return roots
}

func checkUnconfiguredLibrary(database, videos string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(database + suffix); err == nil {
			return fmt.Errorf("已有数据库资料，请在存储设置中选择已有数据库并确认视频保留方式：%s", database)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := workfiles.CheckFutureDir(videos); err != nil {
		return err
	}
	entries, err := os.ReadDir(videos)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && cacheName.MatchString(entry.Name()) {
			return fmt.Errorf("已有完整视频，请在存储设置中选择数据库并确认视频保留方式：%s", videos)
		}
	}
	return nil
}
