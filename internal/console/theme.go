package console

// Theme changes persist only this preference, retaining all saved runtime
// settings and any pending restart. Use the same atomic save lifecycle.
func (c *settingsEditor) saveTheme(theme string) error {
	if err := validateTheme(theme); err != nil {
		return rejectRequest(err)
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	s := c.savedSettings
	c.mu.Unlock()
	s.Theme = theme
	s.themeFallback = false
	return c.saveSettingsLocked(s, true, false, nil)
}
