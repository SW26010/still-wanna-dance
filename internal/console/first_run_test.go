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

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

func TestFirstRunLibraryAvailableWithoutAcceleration(t *testing.T) {
	for _, name := range []string{"missing_config", "unconfigured_v7"} {
		t.Run(name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			if name == "unconfigured_v7" {
				if err := os.WriteFile(config, []byte(`{"schemaVersion":7,"databasePath":"","videoDir":"","theme":"dark"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := newConsole(config, "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if c.store == nil || c.storeOpenError != nil || c.settings.DatabasePath != c.defaultDatabasePath() || c.settings.VideoDir != c.defaultVideoDir() || c.savedSettings != c.settings {
				t.Fatal("default storage was not ready at application startup", c.storeOpenError)
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
			c.syncMonitorCatalogs(t.Context(), []upstreamstate.CatalogResponse{monitorFull("kiva", "20261026000000", "first song", strings.Repeat("a", 32))})
			call("POST", "/api/songs", fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(c.settings.DatabasePath)))
			var page cacheproxy.SongPage
			if err := json.Unmarshal(call("GET", "/api/songs?policy=favorite", "").Body.Bytes(), &page); err != nil || page.Total != 1 {
				t.Fatal("fresh library could not save a favorite", page, err)
			}
			if c.service != nil || c.httpServer != nil || c.queue.Running || c.batch.Running {
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
			if c.store != nil || c.storeOpenError == nil || c.settings.DatabasePath != database {
				t.Fatal("configured database failure was hidden", c.storeOpenError)
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
	for _, name := range []string{"data/database/storage.sqlite-wal", "data/database/storage.sqlite-journal", "videos/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4", "storage/storage.sqlite", "storage/storage.sqlite-shm", "storage/videos/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4"} {
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
			if c.store != nil || c.storeOpenError == nil {
				t.Fatal("existing data was implicitly adopted")
			}
			if _, err := os.Stat(c.defaultDatabasePath()); !os.IsNotExist(err) {
				t.Fatal("replacement database created", err)
			}
			if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, original) {
				t.Fatal("existing data changed", err)
			}
		})
	}
}

func TestPreviousLayoutGuardProtectsOnlyKnownData(t *testing.T) {
	for _, layout := range []string{"default", "custom"} {
		for _, evidence := range []string{"config", "database", "wal", "video", "unreadable-video-directory", "empty-and-locks"} {
			if layout == "custom" && evidence == "config" {
				continue
			}
			t.Run(layout+"/"+evidence, func(t *testing.T) {
				root := t.TempDir()
				exe := filepath.Join(root, "SWD.exe")
				config := apppaths.DefaultConfig(exe)
				previous := filepath.Join(root, "data")
				if layout == "custom" {
					config = filepath.Join(root, "profiles", "dance.json")
					previous = config + ".d"
				}
				files := map[string]string{
					"config": "config.json", "database": "storage/storage.sqlite", "wal": "storage/storage.sqlite-wal",
					"video":                      "storage/videos/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4",
					"unreadable-video-directory": "storage/videos", "empty-and-locks": "storage/.lock",
				}
				path := filepath.Join(previous, files[evidence])
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				original := []byte("previous data must survive")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				if evidence == "empty-and-locks" {
					if err := os.MkdirAll(filepath.Join(previous, "storage", "videos"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				err := checkPreviousLibrary(config, exe)
				if (err == nil) != (evidence == "empty-and-locks") {
					t.Fatal("wrong previous-layout decision", err)
				}
				if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, original) {
					t.Fatal("previous evidence modified", err)
				}
				if _, err := os.Stat(filepath.Join(apppaths.ForConfig(config).Root, "data", "database")); !os.IsNotExist(err) {
					t.Fatal("guard created new library", err)
				}
			})
		}
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
	if c.store == nil || c.storeOpenError != nil || c.settings.DatabasePath != database {
		t.Fatal("old residue blocked the selected library", c.storeOpenError)
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
	c.settings.LogDir, c.settings.ManualLogDir = t.TempDir(), true
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
