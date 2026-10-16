package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

func TestStatusDerivedSettingsRefreshOnSaveAndExplicitKeyCheck(t *testing.T) {
	c := testConsole(t)
	type state struct {
		Version string          `json:"setupSettingsVersion"`
		Error   string          `json:"setupSettingsError"`
		Nodes   proxyNodeStatus `json:"proxyNodes"`
	}
	read := func(c *Console) state {
		t.Helper()
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
		var result state
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatal(w.Code, err)
		}
		return result
	}
	initial := read(c)
	if initial.Version == "" || initial.Error != "" {
		t.Fatal(initial)
	}
	key, err := os.ReadFile(c.files().ChannelKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.files().ChannelKey, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	// Polling does not reopen the key or recompute a verifier for unchanged settings.
	for range 3 {
		if got := read(c); got != initial {
			t.Fatal("status reread the key", got)
		}
	}
	if _, err := c.setupSettingsVersion(c.savedSettings); err == nil {
		t.Fatal("explicit validation reused the cached key")
	}
	if got := read(c); got.Error == "" || got.Version != "" {
		t.Fatal("explicit key error was not published", got)
	}
	if err := os.WriteFile(c.files().ChannelKey, key, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.setupSettingsVersion(c.savedSettings); err != nil {
		t.Fatal(err)
	}
	imported, err := c.importProxyNodes([]byte(nodeFixture))
	if err != nil {
		t.Fatal(err)
	}
	s := c.savedSettings
	s.nodeImportID = imported.ID
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	saved := read(c)
	if saved.Version == initial.Version || saved.Error != "" || saved.Nodes.Saved != 1 || saved.Nodes.Loaded != 0 {
		t.Fatal("saved/loaded derived state did not follow config", saved)
	}
	s = c.savedSettings
	s.PrepareAheadSeconds = -1
	if err := c.save(s); err == nil {
		t.Fatal("invalid save accepted")
	}
	if got := read(c); got != saved {
		t.Fatal("failed save changed derived state", got)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if got := read(loaded); got.Version != saved.Version || got.Nodes.Saved != 1 || got.Nodes.Loaded != 1 {
		t.Fatal("reload lost derived state", got)
	}
}
