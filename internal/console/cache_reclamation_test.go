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
			next.AggressiveCacheReclamation = policy
			next.VideoStorageMode, next.MaxCacheBytes = mode, 5<<30
			data, _ := json.Marshal(next)
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(data))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 200 || c.savedSettings != next || c.settings.AggressiveCacheReclamation != "auto" {
				t.Fatalf("save %s/%s: %d %s", policy, mode, w.Code, w.Body.String())
			}
			_, fields, err := readConfigFile(c.configPath)
			if err != nil || string(fields["aggressiveCacheReclamation"]) != `"`+policy+`"` {
				t.Fatal("strategy was not stored explicitly", fields, err)
			}
			loaded, err := New(c.configPath, c.address)
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

func TestCacheReclamationRejectsInvalidSettingsAndSetup(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"", "true", "false", "unknown"} {
		next := c.settings
		next.AggressiveCacheReclamation = policy
		if err := c.save(next); err == nil {
			t.Fatalf("accepted policy %q", policy)
		}
		plan := setupTestPlan(c)
		plan.AggressiveCacheReclamation = policy
		if _, err := c.validateSetupPlan(plan); err == nil {
			t.Fatalf("wizard accepted policy %q", policy)
		}
	}
	after, err := os.ReadFile(c.configPath)
	if err != nil || !bytes.Equal(before, after) || c.savedSettings != c.settings {
		t.Fatal("invalid settings changed configuration", err)
	}
}

func TestSetupCacheReclamationPersistsAndRestarts(t *testing.T) {
	for _, policy := range []string{"auto", "on", "off"} {
		t.Run(policy, func(t *testing.T) {
			c := testConsole(t)
			p := setupTestPlan(c)
			p.AggressiveCacheReclamation = policy
			p.VideoStorageMode, p.MaxCacheBytes = "limited", 5<<30
			a, err := c.applySetupPlan(p)
			if err != nil || !a.ConfigSaved || c.savedSettings.AggressiveCacheReclamation != policy || c.settings.AggressiveCacheReclamation != "auto" {
				t.Fatalf("wizard: %+v %v", a, err)
			}
			c = restartTestConsole(t, c)
			if c.settings.AggressiveCacheReclamation != policy || c.settings.VideoStorageMode != "limited" {
				t.Fatal("wizard strategy lost on restart", c.settings)
			}
		})
	}
}
