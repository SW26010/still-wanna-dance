package console

import "testing"

func TestCacheLimitSettingsPersist(t *testing.T) {
	c := testConsole(t)
	settings := c.settings
	settings.MaxCacheBytes = 5 << 30
	if err := c.save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.MaxCacheBytes != settings.MaxCacheBytes {
		t.Fatal("limit lost on restart")
	}
	settings.MaxCacheBytes = -1
	if err := c.save(settings); err == nil {
		t.Fatal("negative limit accepted")
	}
	if c.savedSettings.MaxCacheBytes != 5<<30 {
		t.Fatal("invalid setting changed saved limit")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureEngine(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRetentionSettings(t *testing.T) {
	c := testConsole(t)
	if c.settings.RequestRetentionDays != 30 {
		t.Fatal("missing default retention")
	}
	for _, days := range []int{7, 0} {
		settings := c.settings
		settings.RequestRetentionDays = days
		if err := c.save(settings); err != nil {
			t.Fatal(err)
		}
		loaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.settings.RequestRetentionDays != days {
			t.Fatal("retention lost on restart")
		}
		loaded.Close()
	}
	for _, days := range []int{-1, 36501} {
		settings := c.settings
		settings.RequestRetentionDays = days
		if err := c.save(settings); err == nil {
			t.Fatal("invalid retention accepted")
		}
	}
}
