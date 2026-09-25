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
	if c.settings.MaxCacheBytes != 5<<30 {
		t.Fatal("invalid setting changed saved limit")
	}
	if err := c.ensureEngine(); err != nil {
		t.Fatal(err)
	}
}
