package console

// The first status read initializes the verifier lazily, preserving the rule
// that an unused wizard does not create channel.key. Subsequent polls reuse the
// configuration-derived result. Saves refresh an initialized cache; wizard
// validation/apply/resume explicitly reread the key via setupSettingsVersion.
type settingsVersionCache struct {
	ready    bool
	settings Settings
	version  string
	err      error
}

func (c *Console) computeSettingsVersionLocked(s Settings) (string, error) {
	version, err := c.computeSetupSettingsVersion(s)
	c.setupVersion = settingsVersionCache{true, s, version, err}
	return version, err
}

func (c *Console) cachedSettingsVersion(s Settings) (string, error) {
	c.setupVersionMu.Lock()
	defer c.setupVersionMu.Unlock()
	if !c.setupVersion.ready || c.setupVersion.settings != s {
		return c.computeSettingsVersionLocked(s)
	}
	return c.setupVersion.version, c.setupVersion.err
}

func (c *Console) refreshSettingsVersionIfUsed(s Settings) {
	c.setupVersionMu.Lock()
	defer c.setupVersionMu.Unlock()
	if c.setupVersion.ready {
		c.computeSettingsVersionLocked(s)
	}
}
