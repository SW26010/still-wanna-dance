package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageChoiceChecksSelectedRoots(t *testing.T) {
	c := testConsole(t)
	portable, root := t.TempDir(), t.TempDir()
	c.identity.Executable = filepath.Join(portable, "SWD.exe")
	base := c.settings
	base.DatabasePath = filepath.Join(root, "library.sqlite")
	base.VideoDir = filepath.Join(root, "videos")
	base.BlockCacheDir = filepath.Join(root, "cache")
	for _, tc := range []struct {
		name, choice string
		change       func(*Settings)
		wantError    bool
	}{
		{"separate siblings", "cache", func(*Settings) {}, false},
		{"sibling prefix", "video", func(s *Settings) { s.VideoDir = s.BlockCacheDir + "-videos" }, false},
		{"same directories", "cache", func(s *Settings) { s.BlockCacheDir = s.VideoDir }, true},
		{"cache contains videos", "cache", func(s *Settings) { s.BlockCacheDir = root }, true},
		{"videos contain cache", "video", func(s *Settings) { s.VideoDir = root }, true},
		{"database in videos", "database", func(s *Settings) { s.DatabasePath = filepath.Join(s.VideoDir, "library.sqlite") }, true},
		{"video contains database", "video", func(s *Settings) { s.DatabasePath = filepath.Join(s.VideoDir, "library.sqlite") }, true},
		{"database in cache", "database", func(s *Settings) { s.DatabasePath = filepath.Join(s.BlockCacheDir, "library.sqlite") }, true},
		{"cache contains database", "cache", func(s *Settings) { s.DatabasePath = filepath.Join(s.BlockCacheDir, "library.sqlite") }, true},
		{"custom cache in portable", "cache", func(s *Settings) { s.BlockCacheDir = filepath.Join(portable, "other") }, true},
		{"portable directory itself", "cache", func(s *Settings) { s.BlockCacheDir = portable }, true},
		{"default cache", "cache", func(s *Settings) { s.BlockCacheDir = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.change(&s)
			err := c.validateStorageChoice(s, tc.choice)
			if (err != nil) != tc.wantError {
				t.Fatalf("want error %v, got %v", tc.wantError, err)
			}
		})
	}
}

func TestStorageSelectionRejectsConflictBeforeCreatingDatabase(t *testing.T) {
	c := testConsole(t)
	cache := t.TempDir()
	database := filepath.Join(cache, "new", "library.sqlite")
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"databasePath": database, "videoDir": c.settings.VideoDir, "blockCacheDir": cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/prepare-database", bytes.NewReader(body))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "数据库文件不能位于临时视频缓存目录内") {
		t.Fatal("database inside draft cache was accepted", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatal("rejected database created its parent", err)
	}
	if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected database changed the configuration", err)
	}
}

func TestStorageDraftValidationRetainsOtherConflicts(t *testing.T) {
	c := testConsole(t)
	portable := t.TempDir()
	c.identity.Executable = filepath.Join(portable, "SWD.exe")
	badCache := filepath.Join(portable, "custom")
	video := filepath.Join(t.TempDir(), "videos")
	check := func(cache, choice string) (bool, string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"databasePath": c.settings.DatabasePath, "videoDir": video, "blockCacheDir": cache, "selection": choice,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/validate-location", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var result struct {
			Valid bool   `json:"valid"`
			Field string `json:"field"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid validation response", w.Code, w.Body.String())
		}
		return result.Valid, result.Field
	}
	if valid, field := check(badCache, "cache"); valid || field != "cache" {
		t.Fatal("custom Portable cache was accepted", valid, field)
	}
	if valid, field := check(badCache, "video"); valid || field != "cache" {
		t.Fatal("another selection cleared the existing cache conflict", valid, field)
	}
	if valid, field := check(t.TempDir(), "cache"); !valid || field != "" {
		t.Fatal("corrected draft remained invalid", valid, field)
	}
}

func TestStorageCreationWaitsForOtherDraftLocations(t *testing.T) {
	c := testConsole(t)
	portable := t.TempDir()
	c.identity.Executable = filepath.Join(portable, "SWD.exe")
	database := filepath.Join(t.TempDir(), "new", "library.sqlite")
	prepare := func(cache string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"databasePath": database, "videoDir": c.settings.VideoDir, "blockCacheDir": cache,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/prepare-database", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if w := prepare(filepath.Join(portable, "custom")); w.Code != 400 {
		t.Fatal("created a database while another draft location was invalid", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatal("invalid draft created the database parent", err)
	}
	if w := prepare(t.TempDir()); w.Code != 200 || !strings.Contains(w.Body.String(), `"created":true`) {
		t.Fatal("corrected draft did not allow database creation", w.Code, w.Body.String())
	}
}

func TestStorageCreationAvoidsSavedAndActiveLocations(t *testing.T) {
	for _, snapshot := range []string{"active", "saved"} {
		t.Run(snapshot, func(t *testing.T) {
			c := testConsole(t)
			oldVideo := t.TempDir()
			if snapshot == "active" {
				c.settings.VideoDir = oldVideo
			} else {
				c.savedSettings.VideoDir = oldVideo
			}
			database := filepath.Join(oldVideo, "new", "library.sqlite")
			body, err := json.Marshal(map[string]string{
				"databasePath": database, "videoDir": filepath.Join(t.TempDir(), "videos"), "blockCacheDir": t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/prepare-database", bytes.NewReader(body))
			r.Header.Set("X-SWD-Token", c.token)
			w := httptest.NewRecorder()
			c.ServeHTTP(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "已保存或正在使用") {
				t.Fatalf("created in %s location: %d %s", snapshot, w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
				t.Fatal("rejected database created its parent", err)
			}
		})
	}
}

func TestStorageSaveRejectsNewOverlapWithoutChangingConfig(t *testing.T) {
	c := testConsole(t)
	next := c.savedSettings
	// The old rule checked only cache/<instance>, which is a sibling of videos.
	next.BlockCacheDir = filepath.Dir(next.VideoDir)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.save(next); err == nil || !strings.Contains(err.Error(), "互相包含") {
		t.Fatal("saved a cache parent containing the video directory", err)
	}
	if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(before, after) || c.savedSettings.BlockCacheDir != "" {
		t.Fatal("rejected cache changed saved settings", err)
	}
}

func TestStorageSaveRechecksLatestSavedPaths(t *testing.T) {
	c := testConsole(t)
	cache := t.TempDir()
	next := c.savedSettings
	next.BlockCacheDir = cache
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(cache, "videos")
	body, err := json.Marshal(map[string]string{"videoDir": video})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", bytes.NewReader(body))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "互相包含") {
		t.Fatal("stale partial update bypassed the latest cache selection", w.Code, w.Body.String())
	}
	if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(before, after) || c.savedSettings.BlockCacheDir != cache {
		t.Fatal("rejected update changed the latest configuration", err)
	}
	if _, err := os.Stat(video); !os.IsNotExist(err) {
		t.Fatal("rejected update created a video directory", err)
	}
}

func TestLegacyOverlappingSelectionsAllowUnrelatedSaves(t *testing.T) {
	c := testConsole(t)
	legacy := c.settings
	legacy.BlockCacheDir = filepath.Dir(legacy.VideoDir)
	if err := saveConfigSettings(c.configPath, legacy); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.settings.BlockCacheDir != legacy.BlockCacheDir || c.store == nil {
		t.Fatal("legacy selection failed to reopen", c.storageOpenError)
	}
	unrelated := c.savedSettings
	unrelated.LogLevel = "debug"
	if err := c.save(unrelated); err != nil {
		t.Fatal("new selection rules blocked an unrelated save", err)
	}
	if c.savedSettings.BlockCacheDir != legacy.BlockCacheDir || c.savedSettings.VideoDir != legacy.VideoDir {
		t.Fatal("unrelated save changed legacy paths")
	}
}

func TestLegacySelectionValidationChecksOnlyChangedLocations(t *testing.T) {
	c := testConsole(t)
	legacy := c.savedSettings
	legacy.BlockCacheDir = filepath.Dir(legacy.VideoDir)
	c.savedSettings = legacy
	check := func(s Settings, selection string) (bool, string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"databasePath": s.DatabasePath, "videoDir": s.VideoDir, "blockCacheDir": s.BlockCacheDir, "selection": selection,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/validate-location", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var result struct {
			Valid bool   `json:"valid"`
			Field string `json:"field"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid validation response", w.Code, w.Body.String())
		}
		return result.Valid, result.Field
	}
	for _, selection := range []string{"video", "cache"} {
		if valid, field := check(legacy, selection); !valid {
			t.Fatalf("unchanged %s selection was rejected at %s", selection, field)
		}
	}
	changed := legacy
	changed.DatabasePath = filepath.Join(legacy.VideoDir, "other.sqlite")
	if valid, field := check(changed, "video"); valid || field != "database" {
		t.Fatal("unchanged selection hid a different invalid change", valid, field)
	}
}

func TestLegacyDatabaseReselectionStillChecksTheFile(t *testing.T) {
	c := testConsole(t)
	legacy := c.savedSettings
	legacy.DatabasePath = filepath.Join(legacy.VideoDir, "legacy.sqlite")
	createTestDatabase(t, legacy.DatabasePath)
	c.savedSettings = legacy
	prepare := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"databasePath": path, "videoDir": legacy.VideoDir, "blockCacheDir": legacy.BlockCacheDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/prepare-database", bytes.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if w := prepare(legacy.DatabasePath); w.Code != 200 || !strings.Contains(w.Body.String(), `"created":false`) {
		t.Fatal("existing legacy database was rejected", w.Code, w.Body.String())
	}
	if err := os.Remove(legacy.DatabasePath); err != nil {
		t.Fatal(err)
	}
	if w := prepare(legacy.DatabasePath); w.Code != 400 || !strings.Contains(w.Body.String(), "数据库文件丢失") {
		t.Fatal("missing legacy database was not rejected", w.Code, w.Body.String())
	}
	if _, err := os.Stat(legacy.DatabasePath); !os.IsNotExist(err) {
		t.Fatal("missing legacy database was recreated", err)
	}
}

func TestLegacyPortableCacheAllowsUnrelatedSaves(t *testing.T) {
	c := testConsole(t)
	portable := t.TempDir()
	c.identity.Executable = filepath.Join(portable, "SWD.exe")
	legacy := c.settings
	legacy.BlockCacheDir = filepath.Join(portable, "old-cache")
	if err := os.Mkdir(legacy.BlockCacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigSettings(c.configPath, legacy); err != nil {
		t.Fatal(err)
	}
	c.settings, c.savedSettings = legacy, legacy
	unrelated := legacy
	unrelated.LogLevel = "debug"
	if err := c.save(unrelated); err != nil {
		t.Fatal("legacy cache blocked an unrelated save", err)
	}
	if c.savedSettings.BlockCacheDir != legacy.BlockCacheDir {
		t.Fatal("unrelated save changed the legacy cache path")
	}
}

func TestLegacyDatabaseInVideoAllowsUnrelatedSaves(t *testing.T) {
	c := testConsole(t)
	legacy := c.settings
	legacy.DatabasePath = filepath.Join(legacy.VideoDir, "library.sqlite")
	createTestDatabase(t, legacy.DatabasePath)
	if err := saveConfigSettings(c.configPath, legacy); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.settings.DatabasePath != legacy.DatabasePath || c.store == nil {
		t.Fatal("legacy database failed to reopen", c.storageOpenError)
	}
	unrelated := c.savedSettings
	unrelated.LogLevel = "debug"
	if err := c.save(unrelated); err != nil {
		t.Fatal("legacy database blocked an unrelated save", err)
	}
	if c.savedSettings.DatabasePath != legacy.DatabasePath {
		t.Fatal("unrelated save changed the legacy database path")
	}
}
