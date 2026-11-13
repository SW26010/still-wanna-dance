package console

import (
	"errors"
	"fmt"
	"log/slog"
)

// Resource checks and preparation belong to the application, not file decoding.
func (c *settingsEditor) resolveSettings(s Settings) (Settings, error) {
	s, err := resolveConfigSettings(s, c.configPath)
	if err != nil {
		return s, err
	}
	return c.resolveResourceSettings(s)
}

// Selecting or creating a storage location does not depend on unrelated draft
// values. Full settings validation still runs before committing configuration.
func (c *settingsEditor) resolveResourceSettings(s Settings) (Settings, error) {
	if s.BlockCacheDir != "" && sameResourcePath(s.BlockCacheDir, defaultBlockCacheDir(c.identity.Executable)) {
		s.BlockCacheDir = ""
	}
	if err := c.validateResourcePaths(s); err != nil {
		return s, err
	}
	return s, invalidSetting(c.validateBlockCacheSelection(s), "blockCacheDir")
}

func (c *settingsEditor) save(s Settings) error {
	return c.saveSettings(s, false, nil)
}

func (c *settingsEditor) saveSettingsPatch(data []byte) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	saved := c.savedSettings
	c.mu.Unlock()
	input, err := decodeSettingsRequest(data, saved)
	if err != nil {
		return rejectRequest(err)
	}
	return c.saveSettingsLocked(input.Settings, input.Password == nil, input.ConfirmVideoPolicy)
}

// An omitted password in the settings API preserves the stored secret. Merge
// under the lifecycle lock so concurrent saves cannot restore stale secrets.
func (c *settingsEditor) saveSettings(s Settings, preservePassword bool, confirmVideoPolicy *videoPolicyConfirmation) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.saveSettingsLocked(s, preservePassword, confirmVideoPolicy)
}

func (c *settingsEditor) saveSettingsLocked(s Settings, preservePassword bool, confirmVideoPolicy *videoPolicyConfirmation) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return rejectRequest(errors.New("程序正在退出"))
	}
	oldSettings := c.savedSettings
	c.mu.Unlock()
	// Ordinary forms omit the independently saved theme, so a stale form cannot
	// overwrite a newer appearance choice from another page or browser.
	if s.Theme == "" {
		s.Theme = oldSettings.Theme
	}
	if preservePassword {
		s.SOCKS5Password = oldSettings.SOCKS5Password
		s.proxySecret = oldSettings.proxySecret
		s.proxySecretError = oldSettings.proxySecretError
	} else {
		s.proxySecret = ""
		s.proxySecretError = ""
	}
	var err error
	s, err = c.mergeProxyNodes(s, oldSettings)
	if err != nil {
		return err
	}
	importID := s.nodeImportID
	s.nodeImportID = ""
	s, err = c.resolveSettings(s)
	if err != nil {
		return err
	}
	if err = c.validateChangedStorageChoices(s, oldSettings); err != nil {
		return err
	}
	if err = validateProxyNodeSelection(s); err != nil {
		return err
	}
	if err := c.prepareBlockCacheSelection(s, oldSettings); err != nil {
		return err
	}
	if err := c.prepareLibrarySelection(s, oldSettings, confirmVideoPolicy); err != nil {
		return err
	}
	if s.RestoreHostsOnStopOrExit && !oldSettings.RestoreHostsOnStopOrExit {
		if err = c.hosts.prepareRestore(); err != nil {
			return fmt.Errorf("未保存设置：无法准备退出时恢复游戏直连的权限：%w", err)
		}
	}
	c.mu.Lock()
	activeCacheDir := c.settings.BlockCacheDir
	c.mu.Unlock()
	var previousRoot *string
	if !sameResourcePath(s.BlockCacheDir, activeCacheDir) {
		previousRoot = &activeCacheDir
	}
	if err = saveConfigSettingsWithCleanup(c.configPath, s, previousRoot); err != nil {
		return err
	}
	nodeCount := configurationNodeCount(s.nodeSnapshot)
	commit := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.savedSettings = s
		c.settings.RestoreHostsOnStopOrExit = s.RestoreHostsOnStopOrExit
		c.settings.Theme = s.Theme
		// Display metadata is saved immediately and does not require a core restart.
		c.settings.nodeInfo = s.nodeInfo
		c.savedNodeCount = nodeCount
		delete(c.proxyImports, importID)
	}
	if c.onSaved != nil {
		c.onSaved(s, commit)
	} else {
		commit()
	}
	slog.Info("设置已保存", "event_name", "settings_saved", "database_path", s.DatabasePath, "video_dir", s.VideoDir, "vrchat_log_dir", s.effectiveVRChatLogDir())
	return nil
}
