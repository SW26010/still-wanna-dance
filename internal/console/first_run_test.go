package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/upstreamstate"
)

func TestFirstRunLibraryAvailableWithoutAcceleration(t *testing.T) {
	for _, name := range []string{"missing_config", "unconfigured_v7"} {
		t.Run(name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if name == "unconfigured_v7" {
				if err := os.WriteFile(config, []byte(`{"schemaVersion":8,"databasePath":"","videoDir":"","theme":"dark"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := newConsole(config, "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if c.store == nil || c.storageOpenError != nil || c.settings.DatabasePath != c.defaultDatabasePath() || c.settings.VideoDir != c.defaultVideoDir() || c.savedSettings != c.settings {
				t.Fatal("default storage was not ready at application startup", c.storageOpenError)
			}
			if name == "unconfigured_v7" && c.settings.Theme != "dark" {
				t.Fatal("initialization reset other settings")
			}
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
			call := func(method, path, body string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(body))
				r.Header.Set("X-SWD-Token", c.token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				return w
			}
			// The real monitor callback and ordinary library actions share the
			// default database before any engine or playback listener exists.
			call("GET", "/api/songs", "")
			c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{monitorFull("kiva", "20261026000000", "first song", strings.Repeat("a", 32))})
			call("POST", "/api/songs", fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(c.settings.DatabasePath)))
			var page cacheproxy.SongPage
			if err := json.Unmarshal(call("GET", "/api/songs?policy=favorite", "").Body.Bytes(), &page); err != nil || page.Total != 1 {
				t.Fatal("fresh library could not save a favorite", page, err)
			}
			if c.service != nil || c.httpServer != nil || c.queue.snapshot().Running || c.batch.Running {
				t.Fatal("business storage initialization started video work")
			}
			if c.localInventory().Catalog.Revision != "20261026000000" {
				t.Fatal("background catalog update required video startup")
			}
			c = restartTestConsole(t, c)
			if err := json.Unmarshal(call("GET", "/api/songs?policy=favorite", "").Body.Bytes(), &page); err != nil || page.Total != 1 {
				t.Fatal("reopening lost the default library", page, err)
			}
		})
	}
}

func TestFirstRunDoesNotReplaceConfiguredDatabase(t *testing.T) {
	for _, state := range []string{"missing", "empty", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			database, config := c.settings.DatabasePath, c.configPath
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			var original []byte
			if state == "missing" {
				err = os.Remove(database)
			} else {
				if state == "corrupt" {
					original = []byte("damaged database kept for recovery")
				}
				err = os.WriteFile(database, original, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			c, err = newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.store != nil || c.storageOpenError == nil || c.settings.DatabasePath != database {
				t.Fatal("configured database failure was hidden", c.storageOpenError)
			}
			data, err := os.ReadFile(database)
			if state == "missing" {
				if !os.IsNotExist(err) {
					t.Fatal("missing configured database was recreated", err)
				}
			} else if err != nil || !bytes.Equal(data, original) {
				t.Fatal("existing database was overwritten", err)
			}
		})
	}
}

func TestFirstRunPreservesUnconfiguredExistingData(t *testing.T) {
	for _, name := range []string{"data/database/storage.sqlite", "data/database/storage.sqlite-wal", "data/database/storage.sqlite-journal", "videos/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("existing data")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, filepath.Join(root, "config.json"), "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if c.store != nil || c.storageOpenError != nil || c.storageConfirmation == nil {
				t.Fatal("existing data was implicitly adopted")
			}
			if _, err := os.Stat(c.defaultDatabasePath()); path != c.defaultDatabasePath() && !os.IsNotExist(err) {
				t.Fatal("replacement database created", err)
			}
			_, storeErr := c.businessStore()
			if storeErr == nil || strings.Contains(storeErr.Error(), "数据库不可用") || !strings.Contains(storeErr.Error(), "尚未指定数据库和完整视频目录") {
				t.Fatal("storage confirmation was reported as corruption", storeErr)
			}
			if inventory := c.localInventory(); inventory.Error != "" || inventory.CoverageKnown || !inventory.Updated.IsZero() || inventory.Catalog.Revision != "" {
				t.Fatal("unconfirmed database produced a catalog failure or fabricated coverage", inventory)
			}
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
			var status struct{ StorageError, StorageConfirmation string }
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.StorageError != "" || status.StorageConfirmation != storeErr.Error() {
				t.Fatalf("storage confirmation status: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, original) {
				t.Fatal("existing data changed", err)
			}
		})
	}
}

func TestConfiguredLibraryIgnoresPreviousLayoutResidue(t *testing.T) {
	c, err := newConsoleWithWorkForTest(t, filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	database := c.settings.DatabasePath
	old := filepath.Join(c.files().Root, "storage", "storage.sqlite")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old data"), 0600); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	defer c.Close()
	if c.store == nil || c.storageOpenError != nil || c.settings.DatabasePath != database {
		t.Fatal("old residue blocked the selected library", c.storageOpenError)
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "old data" {
		t.Fatal("old data changed", err)
	}
}

func TestFirstRunAccelerationNeedsNoStorageSetup(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	c, err := newConsole(config, "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.hosts, _ = fixtureHostsController(t, hostsBaseline)
	c.videoAddress, c.httpsAddress, c.checksumURL = "127.0.0.1:0", "127.0.0.1:0", ""
	c.settings.VRChatLogDir = t.TempDir()
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
		t.Fatalf("first-run one-click acceleration required manual storage setup: %v", err)
	}
	if c.activation.Phase != "waiting" || c.httpServer == nil || c.store == nil {
		t.Fatal("first-run acceleration did not become ready", c.activation)
	}
}

func TestMissingStorageChoicesInitializeOnlySafeDefaults(t *testing.T) {
	for _, missing := range []string{"database", "video", "both"} {
		t.Run(missing, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config.json")
			database, videos := filepath.Join(t.TempDir(), "chosen.sqlite"), t.TempDir()
			settings := map[string]any{"schemaVersion": 8, "theme": "dark"}
			if missing == "video" {
				createTestDatabase(t, database)
				settings["databasePath"] = database
			}
			if missing == "database" {
				settings["videoDir"] = videos
			}
			data, _ := json.Marshal(settings)
			if err := os.WriteFile(config, data, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if c.storageOpenError != nil || c.storageConfirmation != nil || c.store == nil {
				t.Fatal("safe defaults failed", c.storageOpenError, c.storageConfirmation)
			}
			if missing != "video" {
				database = c.defaultDatabasePath()
			}
			if missing != "database" {
				videos = c.defaultVideoDir()
			}
			if c.settings.DatabasePath != database || c.settings.VideoDir != videos || c.settings.Theme != "dark" {
				t.Fatal("explicit choice or preference replaced", c.settings)
			}
			saved, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct{ DatabasePath, VideoDir string }
			if err := json.Unmarshal(saved, &persisted); err != nil || persisted.DatabasePath == "" || persisted.VideoDir == "" {
				t.Fatal("resolved defaults not persisted", persisted, err)
			}
			resolved := resolveConfigPaths(Settings{DatabasePath: persisted.DatabasePath, VideoDir: persisted.VideoDir}, config)
			if resolved.DatabasePath != database || resolved.VideoDir != videos {
				t.Fatal("persisted paths resolve to different locations", resolved.DatabasePath, resolved.VideoDir)
			}
			if c.service != nil {
				t.Fatal("initialization started video management")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			c, err = newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			if c.store == nil || c.storageOpenError != nil || c.storageConfirmation != nil || c.settings.DatabasePath != database || c.settings.VideoDir != videos {
				t.Fatal("persisted defaults did not reopen", c.storageOpenError)
			}
		})
	}
}

func TestStoragePreparationSeparatesConfirmationFromFailure(t *testing.T) {
	for _, scenario := range []string{"video_confirmation", "database_confirmation", "video_policy", "database_fault_and_confirmation", "video_fault_and_confirmation", "default_video_failure", "explicit_video_missing", "path_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config.json")
			database := filepath.Join(root, "data", "database", "storage.sqlite")
			videos := filepath.Join(root, "videos")
			selectedDatabase := filepath.Join(t.TempDir(), "selected.sqlite")
			selectedVideos := t.TempDir()
			settings := map[string]any{"schemaVersion": 8}
			wantConfirmation, wantFailure, wantStore := false, false, false
			preserved := ""
			switch scenario {
			case "video_confirmation", "database_fault_and_confirmation":
				settings["databasePath"] = selectedDatabase
				wantConfirmation = true
				if scenario == "video_confirmation" {
					createTestDatabase(t, selectedDatabase)
					wantStore = true
				} else {
					wantFailure = true
				}
				if err := os.MkdirAll(videos, 0700); err != nil {
					t.Fatal(err)
				}
				preserved = filepath.Join(videos, strings.Repeat("a", 32)+".mp4")
			case "database_confirmation", "video_fault_and_confirmation":
				settings["videoDir"] = selectedVideos
				wantConfirmation = true
				if scenario == "video_fault_and_confirmation" {
					settings["videoDir"] = filepath.Join(selectedVideos, "offline")
					wantFailure = true
				}
				if err := os.MkdirAll(filepath.Dir(database), 0700); err != nil {
					t.Fatal(err)
				}
				preserved = database + "-shm"
			case "video_policy":
				settings["videoDir"] = selectedVideos
				preserved = filepath.Join(selectedVideos, strings.Repeat("a", 32)+".mp4")
				wantConfirmation = true
			case "default_video_failure":
				settings["databasePath"] = selectedDatabase
				createTestDatabase(t, selectedDatabase)
				wantStore, wantFailure = true, true
				preserved = videos // A file in place of the default directory.
			case "explicit_video_missing":
				settings["videoDir"] = filepath.Join(selectedVideos, "offline")
				wantFailure = true
			case "path_conflict":
				settings["videoDir"] = filepath.Dir(database)
				if err := os.MkdirAll(filepath.Dir(database), 0700); err != nil {
					t.Fatal(err)
				}
				wantFailure = true
			}
			if preserved != "" {
				if err := os.WriteFile(preserved, []byte("existing data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := json.Marshal(settings)
			if err := os.WriteFile(config, before, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if (c.store != nil) != wantStore || (c.storageOpenError != nil) != wantFailure || (c.storageConfirmation != nil) != wantConfirmation {
				t.Fatalf("store=%v failure=%v confirmation=%v", c.store != nil, c.storageOpenError, c.storageConfirmation)
			}
			if _, err := c.businessStore(); (err == nil) != wantStore {
				t.Fatal("database availability depends on video readiness", err)
			}
			if _, err := os.Stat(database); !os.IsNotExist(err) {
				t.Fatal("default database created despite pending decision/failure", err)
			}
			if after, err := os.ReadFile(config); err != nil || !bytes.Equal(before, after) {
				t.Fatal("unresolved configuration rewritten", err)
			}
			if preserved != "" {
				if after, err := os.ReadFile(preserved); err != nil || string(after) != "existing data" {
					t.Fatal("existing data changed", err)
				}
			}
			if err := c.acceptTerms(); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
			var status struct {
				StorageError, StorageConfirmation string
				StorageIssues                     map[string]storageFieldIssue
			}
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || (status.StorageError != "") != wantFailure || (status.StorageConfirmation != "") != wantConfirmation {
				t.Fatalf("status: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if len(status.StorageIssues) == 0 {
				t.Fatal("storage diagnosis was not attributed to settings fields")
			}
			for field, issue := range status.StorageIssues {
				wantValue := c.settings.DatabasePath
				if field == "videoDir" {
					wantValue = c.settings.VideoDir
				}
				if issue.Message == "" || issue.Value != wantValue {
					t.Fatal("issue lost its cause or active location", field, issue)
				}
			}
			if scenario == "database_fault_and_confirmation" && (status.StorageIssues["databasePath"].Severity != "error" || status.StorageIssues["videoDir"].Severity != "warning") {
				t.Fatal("field severities were merged", status.StorageIssues)
			}
			if scenario == "video_fault_and_confirmation" && (status.StorageIssues["databasePath"].Severity != "warning" || status.StorageIssues["videoDir"].Severity != "error") {
				t.Fatal("field severities were merged", status.StorageIssues)
			}
			c.lifecycleMu.Lock()
			_, engineErr := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if engineErr == nil || c.service != nil {
				t.Fatal("pending/failed storage started video management")
			}
			if !wantFailure && !nonFaultError(engineErr) {
				t.Fatal("confirmation reported as a runtime fault", engineErr)
			}
			if !wantFailure {
				center := faults.New()
				c.SetFaultCenter(center)
				response := httptest.NewRecorder()
				request := httptest.NewRequest("GET", "http://"+c.address+"/api/library", nil)
				request.Header.Set("X-SWD-Token", c.token)
				c.ServeHTTP(response, request)
				if wantStore && response.Code != 200 || !wantStore && (response.Code != 400 || !strings.Contains(response.Body.String(), "尚未指定")) {
					t.Fatal("library did not preserve storage readiness", response.Code, response.Body.String())
				}
				if open, err := center.Take(); open || err != nil {
					t.Fatal("viewing pending storage created a fault incident", open, err)
				}
			}
		})
	}
}
