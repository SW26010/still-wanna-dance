package console

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
)

func TestBlockCacheSelectionStagesAndCleansPreviousWorkspace(t *testing.T) {
	c := testConsole(t)
	exe := filepath.Join(t.TempDir(), "SWD.exe")
	c.identity.Executable = exe
	c.editor.identity = c.identity
	oldWork := apppaths.WorkForConfig(exe, c.configPath)
	if err := os.MkdirAll(oldWork, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(oldWork, "download-block-123.part")
	unknown := filepath.Join(oldWork, "keep.txt")
	for _, path := range []string{block, unknown} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	selected := t.TempDir()
	next := c.settings
	next.BlockCacheDir = selected
	if err := c.editor.save(next); err != nil {
		t.Fatal(err)
	}
	if c.settings.BlockCacheDir != "" || c.editor.savedSettings.BlockCacheDir != selected {
		t.Fatal("save changed the active workspace")
	}
	workPath := apppaths.WorkForCacheRoot(exe, c.configPath, selected)
	if _, err := os.Stat(workPath); !os.IsNotExist(err) {
		t.Fatal("saving created the next workspace", err)
	}
	loaded, err := ReadStartupSettings(c.configPath)
	if err != nil || loaded.BlockCacheDir != selected {
		t.Fatal("selection was not persisted", loaded.BlockCacheDir, err)
	}
	if err := ValidateWorkLocation(exe, c.configPath, loaded); err != nil {
		t.Fatal(err)
	}
	work, err := cacheproxy.OpenWorkDirectory(workPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	CleanupPendingBlockCaches(exe, c.configPath, loaded, work, nil)
	if _, err := os.Stat(block); !os.IsNotExist(err) {
		t.Fatal("previous block was not cleaned", err)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "fixture" {
		t.Fatal("cleanup changed an unknown file", err)
	}
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] != nil {
		t.Fatal("completed cleanup remained pending", err)
	}
}

func TestPreviousBlockCacheCleanupWaitsForOfflineDisk(t *testing.T) {
	c := testConsole(t)
	exe := filepath.Join(t.TempDir(), "SWD.exe")
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	if err := os.Mkdir(oldRoot, 0700); err != nil {
		t.Fatal(err)
	}
	previous := c.settings
	previous.BlockCacheDir = oldRoot
	oldWork := blockWorkPath(exe, c.configPath, previous)
	if err := os.MkdirAll(oldWork, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(oldWork, "download-block-456.part")
	if err := os.WriteFile(block, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	other := apppaths.WorkForCacheRoot(exe, filepath.Join(parent, "other-config.json"), oldRoot)
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	otherBlock := filepath.Join(other, "download-block-789.part")
	if err := os.WriteFile(otherBlock, []byte("other instance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigSettingsWithCleanup(c.configPath, c.settings, &oldRoot); err != nil {
		t.Fatal(err)
	}
	offline := oldRoot + "-offline"
	if err := os.Rename(oldRoot, offline); err != nil {
		t.Fatal(err)
	}
	CleanupPendingBlockCaches(exe, c.configPath, c.settings, nil, nil)
	_, fields, err := readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] == nil {
		t.Fatal("offline cleanup was forgotten", err)
	}
	if _, err := os.Stat(oldRoot); !os.IsNotExist(err) {
		t.Fatal("offline directory was recreated", err)
	}
	if err := os.Rename(offline, oldRoot); err != nil {
		t.Fatal(err)
	}
	CleanupPendingBlockCaches(exe, c.configPath, c.settings, nil, nil)
	if _, err := os.Stat(block); !os.IsNotExist(err) {
		t.Fatal("returning disk was not cleaned", err)
	}
	if data, err := os.ReadFile(otherBlock); err != nil || string(data) != "other instance" {
		t.Fatal("cleanup touched another instance", err)
	}
	_, fields, err = readConfigFile(c.configPath)
	if err != nil || fields[pendingBlockCacheCleanupKey] != nil {
		t.Fatal("completed cleanup was not acknowledged", err)
	}
}

func TestBlockCacheUnavailableAllowsRecovery(t *testing.T) {
	original := testConsole(t)
	root := t.TempDir()
	next := original.settings
	next.BlockCacheDir = root
	if err := original.editor.save(next); err != nil {
		t.Fatal(err)
	}
	config, address := original.configPath, original.address
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	workErr := errors.New("临时缓存盘不可用")
	c, err := newConsoleWithWorkError(config, address, newFixtureDNS(), nil, workErr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.businessStore(); err != nil {
		t.Fatal("cache failure prevented opening the database", err)
	}
	if _, err := c.ensureEngineLocked(); !errors.Is(err, workErr) {
		t.Fatal("video engine ignored the workspace failure", err)
	}
	r := httptest.NewRequest("GET", "http://"+address+"/api/status", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	var status struct {
		WorkError string `json:"workError"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !strings.Contains(status.WorkError, "缓存盘不可用") {
		t.Fatal("cache failure missing from status", w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+address+"/", nil))
	if w.Code != 200 {
		t.Fatal("unavailable workspace prevented the UI from loading", w.Code)
	}
	unrelated := c.editor.savedSettings
	unrelated.LogLevel = "debug"
	if err := c.editor.save(unrelated); err != nil {
		t.Fatal("offline cache blocked an unrelated setting", err)
	}
	recovered := c.editor.savedSettings
	recovered.BlockCacheDir = ""
	if err := c.editor.save(recovered); err != nil {
		t.Fatal("could not select the default while the old disk was offline", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("saving recreated the offline cache root", err)
	}
}

func TestBlockCacheCannotOverlapVideoStorage(t *testing.T) {
	c := testConsole(t)
	roots := []string{c.settings.VideoDir}
	if runtime.GOOS == "windows" {
		roots = append(roots, strings.ToUpper(c.settings.VideoDir))
	}
	for _, root := range roots {
		s := c.settings
		s.BlockCacheDir = root
		if err := c.editor.save(s); err == nil {
			t.Fatal("accepted a workspace inside the video directory", root)
		}
		if _, err := os.Stat(blockWorkPath(c.identity.Executable, c.configPath, s)); !os.IsNotExist(err) {
			t.Fatal("rejected selection created a workspace", err)
		}
	}
}

func TestActivationDeferredCacheChecksKnownDatabaseFailureBeforeHosts(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	database := filepath.Join(root, "missing", "storage.sqlite")
	video, cache := t.TempDir(), t.TempDir()
	data, err := json.Marshal(map[string]any{"schemaVersion": 8, "databasePath": database, "videoDir": video, "blockCacheDir": cache})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := desktop.NewIdentity(config)
	if err != nil {
		t.Fatal(err)
	}
	work, err := cacheproxy.OpenWorkDirectoryDeferred(apppaths.WorkForCacheRoot(identity.Executable, identity.Config, cache), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newConsole(config, "127.0.0.1:0", newFixtureDNS(), work)
	if err != nil {
		work.Close()
		t.Fatal(err)
	}
	c.editor.hosts, _ = fixtureHostsController(t, hostsBaseline)
	defer func() { c.Close(); work.Close() }()
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	var actions []string
	err = c.enableAcceleration(func() HostsStatus { return HostsStatus{} }, func(action string) error {
		actions = append(actions, action)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "数据库不可用") || len(actions) != 0 || !work.CleanupPending() {
		t.Fatal("known database failure touched hosts or cleared unverified work", err, actions)
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("missing database was created", err)
	}
}
