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
	for _, mode := range []string{"none", "limited", "unlimited"} {
		for _, retain := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", mode, retain), func(t *testing.T) {
				c := testConsole(t)
				if !c.settings.RetainFavorites {
					t.Fatal("favorite retention preference should default on")
				}
				c.settings.VideoStorageMode = "unlimited"
				c.savedSettings = c.settings
				openEngine := func() {
					t.Helper()
					c.lifecycleMu.Lock()
					_, err := c.ensureEngineLocked()
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
				file := filepath.Join(testStorageRoot(c.settings), "videos", key+".mp4")
				next := c.savedSettings
				next.VideoStorageMode, next.RetainFavorites = mode, retain
				// The remembered budget must not permit retention in none mode.
				next.VideoBudgetBytes = 1
				if mode == "none" {
					next.VideoBudgetBytes = 1 << 20
				}
				data, _ := json.Marshal(next)
				r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(data))
				r.Header.Set("X-SWD-Token", c.token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code != 200 || c.savedSettings.RetainFavorites != retain || c.savedSettings.VideoStorageMode != mode || c.settings.VideoStorageMode != "unlimited" || !c.settings.RetainFavorites {
					t.Fatal("save changed active retention or lost saved values", w.Code, w.Body.String())
				}
				if _, err := os.Stat(file); err != nil {
					t.Fatal("saving settings removed the active file", err)
				}
				c = restartTestConsole(t, c)
				openEngine()
				if c.settings.RetainFavorites != retain || c.settings.VideoStorageMode != mode || c.settings.VideoBudgetBytes != next.VideoBudgetBytes {
					t.Fatal("restart lost saved preferences")
				}
				wantSaved := mode == "unlimited"
				if data, err := os.ReadFile(file); wantSaved {
					if err != nil || string(data) != body {
						t.Fatal("unlimited retention lost favorite file", err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatal("budgeted retention kept an oversized favorite", err)
				}
				policies, err := c.service.SongPolicies(t.Context())
				if err != nil || policies[42] != cacheproxy.SongFavorite {
					t.Fatal("retention setting changed the favorite mark", policies, err)
				}
				if c.batch.Running || c.queue.snapshot().Running || c.httpServer != nil {
					t.Fatal("retention setting started background work")
				}
			})
		}
	}
}
