package console

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

// This is a request for a storage decision, not a resource failure. It is
// derived at startup and never persisted separately from the selected paths.
type storageConfirmationRequired struct {
	missing, detail string
}

func (e *storageConfirmationRequired) Error() string {
	return "尚未指定" + e.missing + "。" + e.detail + "请在存储设置中确认数据库、完整视频目录和视频保留方式，保存后重启。"
}

// Database access remains independent of video readiness. The startup result
// is fixed until restart; business requests never retry initialization.
func (c *Console) prepareStorage() {
	if c.settings.DatabasePath != "" {
		var err error
		c.store, err = cacheproxy.OpenStore(c.settings.DatabasePath)
		if err != nil {
			c.storageOpenError = withSettingFields(fmt.Errorf("数据库不可用，请在设置中打开已有数据库或明确新建：%w", err), "databasePath")
		}
	}
	if c.settings.DatabasePath == "" || c.settings.VideoDir == "" {
		c.storageOpenError = errors.Join(c.storageOpenError, c.prepareDefaultLibrary())
	}
}

// Fill only missing choices. Validate the final combination and inspect old
// data before creating anything; explicit locations are never replaced.
func (c *Console) prepareDefaultLibrary() error {
	old, s := c.settings, c.settings
	missing := []string{}
	if s.DatabasePath == "" {
		s.DatabasePath = c.editor.defaultDatabasePath()
		missing = append(missing, "数据库")
	}
	if s.VideoDir == "" {
		s.VideoDir = c.editor.defaultVideoDir()
		missing = append(missing, "完整视频目录")
	}
	if err := c.editor.validateResourcePaths(s); err != nil {
		return err
	}
	if err := c.editor.validateChangedStorageChoices(s, old); err != nil {
		return err
	}
	confirm := func(detail string) {
		if c.storageConfirmation == nil {
			c.storageConfirmation = &storageConfirmationRequired{missing: strings.Join(missing, "和"), detail: detail}
		}
	}
	if old.DatabasePath == "" {
		exists, err := unconfiguredDatabaseExists(s.DatabasePath)
		if err != nil {
			return withSettingFields(fmt.Errorf("无法检查默认数据库位置：%w", err), "databasePath")
		}
		if exists {
			confirm(fmt.Sprintf("默认位置发现已有数据库资料：%s。", s.DatabasePath))
		}
	}
	// An explicit offline directory must not be recreated, even when the
	// database is missing. A future default directory may be created below.
	if old.VideoDir != "" {
		if err := workfiles.CheckDir(s.VideoDir); err != nil {
			return withSettingFields(fmt.Errorf("完整视频目录不可用：%w", err), "videoDir")
		}
	} else if err := workfiles.CheckFutureDir(s.VideoDir); err != nil {
		return withSettingFields(fmt.Errorf("默认完整视频目录不可用：%w", err), "videoDir")
	}
	required, err := inspectVideoPolicy(s, old)
	if err != nil {
		return withSettingFields(fmt.Errorf("无法检查完整视频目录：%w", err), "videoDir")
	}
	if required {
		confirm(fmt.Sprintf("完整视频目录已有资料，需要确认其数据库与保留策略：%s。", s.VideoDir))
	}
	if c.storageOpenError != nil || c.storageConfirmation != nil {
		return nil
	}
	if old.VideoDir == "" {
		if err := os.MkdirAll(s.VideoDir, 0700); err != nil {
			return withSettingFields(fmt.Errorf("无法创建默认完整视频目录：%w", err), "videoDir")
		}
		if err := workfiles.CheckDir(s.VideoDir); err != nil {
			return withSettingFields(fmt.Errorf("默认完整视频目录不可用：%w", err), "videoDir")
		}
		if err := workfiles.Probe(s.VideoDir); err != nil {
			return withSettingFields(fmt.Errorf("默认完整视频目录不可用：%w", err), "videoDir")
		}
	}
	if old.DatabasePath == "" {
		store, err := cacheproxy.CreateStore(s.DatabasePath)
		if err != nil {
			return withSettingFields(fmt.Errorf("无法创建默认数据库：%w", err), "databasePath")
		}
		c.store = store
		c.vrcxNotify = true
	}
	c.settings = s
	return nil
}

func unconfiguredDatabaseExists(database string) (bool, error) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(database + suffix); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}
