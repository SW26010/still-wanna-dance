package console

import (
	"errors"
	"fmt"
	"log/slog"
)

// Resource checks and preparation belong to the application, not file decoding.
func (c *Console) resolveSettings(s Settings) (Settings, error) {
	s, err := resolveConfigSettings(s, c.configPath)
	if err == nil {
		err = c.validateResourcePaths(s)
	}
	return s, err
}

func (c *Console) save(s Settings) error {
	return c.saveSettings(s, false, nil)
}

func (c *Console) saveSettingsPatch(data []byte) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	saved := c.savedSettings
	c.mu.Unlock()
	input, err := decodeSettingsRequest(data, saved)
	if err != nil {
		return err
	}
	return c.saveSettingsLocked(input.Settings, input.Password == nil, input.ConfirmVideoPolicy)
}

// An omitted password in the settings API preserves the stored secret. Merge
// under the lifecycle lock so concurrent saves cannot restore stale secrets.
func (c *Console) saveSettings(s Settings, preservePassword bool, confirmVideoPolicy *videoPolicyConfirmation) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.saveSettingsLocked(s, preservePassword, confirmVideoPolicy)
}

func (c *Console) saveSettingsLocked(s Settings, preservePassword bool, confirmVideoPolicy *videoPolicyConfirmation) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return errors.New("程序正在退出")
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
	if err = validateProxyNodeSelection(s); err != nil {
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
	if err = saveConfigSettings(c.configPath, s); err != nil {
		return err
	}
	nodeCount := len(nodeDocument(s.nodeSnapshot).Nodes)
	c.mu.Lock()
	c.savedSettings = s
	c.settings.RestoreHostsOnStopOrExit = s.RestoreHostsOnStopOrExit
	c.settings.Theme = s.Theme
	c.savedNodeCount = nodeCount
	delete(c.proxyImports, importID)
	c.mu.Unlock()
	slog.Info("settings_saved", "database_path", s.DatabasePath, "video_dir", s.VideoDir, "vrchat_log_dir", s.effectiveVRChatLogDir())
	return nil
}
