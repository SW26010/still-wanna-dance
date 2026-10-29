package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCacheReclamationSettingsPersistWithoutChangingRuntime(t *testing.T) {
	c := testConsole(t)
	for _, policy := range []string{"on", "off", "auto"} {
		for _, mode := range []string{"none", "limited", "unlimited"} {
			next := c.settings
			next.AggressiveBlockReclamation = policy
			next.VideoStorageMode, next.VideoBudgetBytes = mode, 5<<30
			data, _ := json.Marshal(next)
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(data))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 200 || c.savedSettings != next || c.settings.AggressiveBlockReclamation != "auto" {
				t.Fatalf("save %s/%s: %d %s", policy, mode, w.Code, w.Body.String())
			}
			_, fields, err := readConfigFile(c.configPath)
			if err != nil || string(fields["aggressiveBlockReclamation"]) != `"`+policy+`"` {
				t.Fatal("strategy was not stored explicitly", fields, err)
			}
			loaded, err := newConsoleWithWorkForTest(t, c.configPath, c.address)
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.settings
			loaded.Close()
			if got != next {
				t.Fatal("load lost strategy or budget", got, next)
			}
		}
	}
}

func TestCacheReclamationRejectsInvalidSettings(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"", "true", "false", "unknown"} {
		next := c.settings
		next.AggressiveBlockReclamation = policy
		if err := c.save(next); err == nil {
			t.Fatalf("accepted policy %q", policy)
		}
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || !bytes.Equal(before, after) || c.savedSettings != c.settings {
		t.Fatal("invalid settings changed configuration", err)
	}
}
