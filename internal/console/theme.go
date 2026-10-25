package console

import "errors"

// Theme changes persist only this preference, retaining all saved runtime
// settings and any pending restart. Use the same atomic save lifecycle.
func (c *Console) saveTheme(theme string) error {
	if theme != "system" && theme != "light" && theme != "dark" {
		return errors.New("主题必须为 system、light 或 dark")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	s := c.savedSettings
	c.mu.Unlock()
	s.Theme = theme
	return c.saveSettingsLocked(s, true)
}
