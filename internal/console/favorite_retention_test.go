package console

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestFavoriteRetentionSettingsApplyAfterRestart(t *testing.T) {
	c := testConsole(t)
	if !c.settings.RetainFavorites {
		t.Fatal("favorite retention should default on")
	}
	openEngine := func() {
		t.Helper()
		c.lifecycleMu.Lock()
		c.mu.Lock()
		err := c.ensureEngine()
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	openEngine()
	const body = "favorite retention settings integration"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	if err := c.service.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261024000000", Source: "test", Songs: []cacheproxy.CatalogSong{{ID: 42, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.service.SetSongPolicy(t.Context(), 42, cacheproxy.SongFavorite); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), key+".mp4")
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.service.ImportVideo(t.Context(), source, map[string]bool{key: true}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(c.settings.StorageDir, "videos", key+".mp4")
	for _, retain := range []bool{false, true} {
		next := c.savedSettings
		next.RetainFavorites = retain
		data, _ := json.Marshal(next)
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(data))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 || c.savedSettings.RetainFavorites != retain || c.settings.RetainFavorites == retain {
			t.Fatal("save changed active retention or lost saved value", w.Code, w.Body.String())
		}
		if !retain {
			if _, err := os.Stat(file); err != nil {
				t.Fatal("saving disabled retention removed active file", err)
			}
		}
		plan, err := c.validateSetupPlan(setupTestPlan(c))
		if err != nil || plan.RetainFavorites != retain {
			t.Fatal("setup did not preserve saved retention", err)
		}
		c = restartTestConsole(t, c)
		openEngine()
		if c.settings.RetainFavorites != retain {
			t.Fatal("restart lost retention setting")
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatal("ordinary zero budget retained or restored the file", err)
		}
		policies, err := c.service.SongPolicies(t.Context())
		if err != nil || policies[42] != cacheproxy.SongFavorite {
			t.Fatal("retention setting changed the favorite mark", policies, err)
		}
		if c.batch.Running || c.queue.Running || c.httpServer != nil {
			t.Fatal("retention setting started background work")
		}
	}
}
