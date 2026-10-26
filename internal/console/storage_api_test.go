package console

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func storageRequest(t *testing.T, c *Console, action string, paths cacheproxy.LibraryPaths) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"databasePath": paths.DatabasePath, "videoDir": paths.VideoDir})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/storage/"+action, bytes.NewReader(body))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestDatabaseCreationIsExplicitAndDoesNotActivateOrOverwrite(t *testing.T) {
	c := testConsole(t)
	paths := cacheproxy.LibraryPaths{DatabasePath: filepath.Join(t.TempDir(), "独立 资料", "library.sqlite"), VideoDir: filepath.Join(t.TempDir(), "完整 视频")}
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	active := c.settings
	if w := storageRequest(t, c, "check", paths); w.Code != 400 {
		t.Fatal("check accepted a missing database", w.Code)
	}
	if _, err := os.Stat(paths.DatabasePath); !os.IsNotExist(err) {
		t.Fatal("check created a database", err)
	}
	if w := storageRequest(t, c, "create-database", paths); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	created, err := os.ReadFile(paths.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if w := storageRequest(t, c, "create-database", paths); w.Code != 400 {
		t.Fatal("creation overwrote an existing file", w.Code)
	}
	if after, err := os.ReadFile(paths.DatabasePath); err != nil || !bytes.Equal(created, after) {
		t.Fatal("repeated creation changed the database", err)
	}
	if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(before, after) || c.settings != active || c.savedSettings != active || c.service != nil {
		t.Fatal("creation activated resources or changed settings", err)
	}
	if _, err := os.Stat(paths.VideoDir); !os.IsNotExist(err) {
		t.Fatal("database creation created videos", err)
	}
	next := active
	next.DatabasePath, next.VideoDir = paths.DatabasePath, paths.VideoDir
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	if c.settings != active {
		t.Fatal("saving activated the new resources")
	}
	c = restartTestConsole(t, c)
	if c.store == nil || c.settings.DatabasePath != paths.DatabasePath || c.settings.VideoDir != paths.VideoDir || c.service != nil {
		t.Fatal("restart did not open the selected database", c.storeOpenError)
	}
	if _, err := os.Stat(paths.VideoDir); err != nil {
		t.Fatal("explicit video selection did not create its directory", err)
	}
}

func TestExistingVideosRequirePolicyConfirmationBeforeChangingDatabase(t *testing.T) {
	c := testConsole(t)
	key := strings.Repeat("a", 32)
	video := filepath.Join(c.settings.VideoDir, key+".mp4")
	if err := os.WriteFile(video, []byte("complete video"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := libraryPaths(c.settings)
	paths.DatabasePath = filepath.Join(t.TempDir(), "other.sqlite")
	created, err := cacheproxy.CreateStore(paths.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	w := storageRequest(t, c, "check", paths)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"confirmVideoPolicy":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	next := c.settings
	next.DatabasePath = paths.DatabasePath
	before, _ := os.ReadFile(c.configPath)
	if err := c.save(next); err == nil {
		t.Fatal("changed database without acknowledging video policy")
	}
	if after, _ := os.ReadFile(c.configPath); !bytes.Equal(before, after) {
		t.Fatal("unconfirmed choice changed configuration")
	}
	if err := c.saveSettings(next, false, true); err != nil {
		t.Fatal(err)
	}
	if c.service != nil {
		t.Fatal("confirmation started video management")
	}
	if data, err := os.ReadFile(video); err != nil || string(data) != "complete video" {
		t.Fatal("saving changed existing video", err)
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields["confirmVideoPolicy"] != nil {
		t.Fatal("confirmation became a persisted setting", err)
	}
}

func TestUnavailableVideoDoesNotPreventUnrelatedSaveOrRecreateRoot(t *testing.T) {
	c := testConsole(t)
	if err := os.Remove(c.settings.VideoDir); err != nil {
		t.Fatal(err)
	}
	next := c.settings
	next.VideoStorageMode = "none"
	next.RequestRetentionDays++
	if err := c.save(next); err != nil {
		t.Fatal("unrelated save depended on the video disk", err)
	}
	if _, err := os.Stat(next.VideoDir); !os.IsNotExist(err) {
		t.Fatal("save recreated an unavailable video root", err)
	}
	next.VideoDir = filepath.Join(t.TempDir(), "replacement")
	if err := c.save(next); err != nil {
		t.Fatal("none mode prevented location changes", err)
	}
	if _, err := os.Stat(next.VideoDir); err != nil {
		t.Fatal(err)
	}
}

func TestMissingVideoSelectionNeverUsesLegacyDefault(t *testing.T) {
	c := testConsole(t)
	next := c.settings
	next.VideoDir = ""
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.store == nil {
		t.Fatal("database depends on a video selection", c.storeOpenError)
	}
	if err := c.start(); err == nil || c.service != nil {
		t.Fatal("unselected video directory started an engine")
	}
}

func TestMissingConfigurationDoesNotAdoptExistingDefaultLibrary(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "data", "database", "storage.sqlite")
	createTestDatabase(t, database)
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newConsoleWithWorkForTest(t, filepath.Join(root, "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.settings.DatabasePath != "" || c.settings.VideoDir != "" || c.store != nil {
		t.Fatal("configuration recovery implicitly adopted a library")
	}
	if after, err := os.ReadFile(database); err != nil || !bytes.Equal(before, after) {
		t.Fatal("default library was modified", err)
	}
	if _, err := os.Stat(filepath.Join(root, "videos")); !os.IsNotExist(err) {
		t.Fatal("video directory created implicitly", err)
	}
}

func TestIndependentPathsResolveRelativeToConfigurationAfterTrimming(t *testing.T) {
	c := testConsole(t)
	next := c.settings
	next.DatabasePath, next.VideoDir = "  songs/library.sqlite  ", "  videos  "
	resolved, err := c.resolveSettings(next)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(c.configPath)
	if resolved.DatabasePath != filepath.Join(base, "songs", "library.sqlite") || resolved.VideoDir != filepath.Join(base, "videos") {
		t.Fatal("paths used the launch directory or retained whitespace", resolved.DatabasePath, resolved.VideoDir)
	}
}
