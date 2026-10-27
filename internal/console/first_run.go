package console

import (
	"fmt"
	"os"

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
	c.vrcxNotify = true
	return nil
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
