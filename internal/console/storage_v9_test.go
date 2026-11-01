package console

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamstate"
)

func copyV8LibraryForUpgrade(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile("../cacheproxy/testdata/storage-v8.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Only this isolated copy replaces the frozen fixture's placeholder URL.
	_, err = db.Exec("UPDATE song_urls SET url='https://example.com/files/1/42-video.mp4?e='||md5||'&s='||byte_size")
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestV8ConfiguredDefaultLibraryUpgradesDuringStartup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data", "database", "storage.sqlite")
	copyV8LibraryForUpgrade(t, path)
	configPath := filepath.Join(root, "config.json")
	config := []byte(`{"schemaVersion":8,"databasePath":"data/database/storage.sqlite","videoDir":"videos"}`)
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := newConsole(configPath, "127.0.0.1:0", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := cacheproxy.ReadResponseSources(t.Context(), path); err != nil || c.service != nil {
		t.Fatal("startup did not produce v9 without an engine", err)
	}
	if data, err := os.ReadFile(configPath); err != nil || !bytes.Equal(data, config) {
		t.Fatal("database migration rewrote configuration", err)
	}
}

func TestV8SelectedLibraryUpgradesOnlyAfterRestart(t *testing.T) {
	for _, existingVideo := range []bool{false, true} {
		name := "empty-video-directory"
		if existingVideo {
			name = "existing-video-policy"
		}
		t.Run(name, func(t *testing.T) {
			c := testConsole(t)
			paths := libraryPaths(c.settings)
			paths.DatabasePath = filepath.Join(t.TempDir(), "selected.sqlite")
			copyV8LibraryForUpgrade(t, paths.DatabasePath)
			before, err := os.ReadFile(paths.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			video := filepath.Join(paths.VideoDir, strings.Repeat("a", 32)+".mp4")
			if existingVideo {
				if err := os.WriteFile(video, []byte("existing video"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if w := storageRequest(t, c, "prepare-database", paths); w.Code != 200 || !strings.Contains(w.Body.String(), `"created":false`) {
				t.Fatal(w.Code, w.Body.String())
			}
			w := storageRequest(t, c, "check", paths)
			var selection struct {
				ConfirmVideoPolicy bool `json:"confirmVideoPolicy"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &selection); err != nil || w.Code != 200 || selection.ConfirmVideoPolicy != existingVideo {
				t.Fatal("selection changed the existing policy confirmation", w.Code, w.Body.String(), err)
			}
			next := c.savedSettings
			next.DatabasePath = paths.DatabasePath
			var confirmed *videoPolicyConfirmation
			if existingVideo {
				if err := c.save(next); err == nil || !strings.Contains(err.Error(), "请先确认此视频目录") {
					t.Fatal("missing video policy confirmation was accepted", err)
				}
				confirmed = &videoPolicyConfirmation{next.DatabasePath, next.VideoDir, next.VideoStorageMode, next.VideoBudgetBytes, next.RetainFavorites}
			}
			if err := c.saveSettings(next, false, confirmed); err != nil {
				t.Fatal(err)
			}
			if after, err := os.ReadFile(paths.DatabasePath); err != nil || !bytes.Equal(before, after) || c.settings.DatabasePath == paths.DatabasePath {
				t.Fatal("selection or save upgraded/activated v8 before restart", err)
			}
			c = restartTestConsole(t, c)
			if _, err := cacheproxy.ReadResponseSources(t.Context(), paths.DatabasePath); err != nil || c.service != nil || c.settings.DatabasePath != paths.DatabasePath {
				t.Fatal("restart did not activate v9 without an engine", err)
			}
			keys, err := c.store.KnownMedia(t.Context())
			if err != nil || len(keys) != 1 {
				t.Fatal("migration changed or reseeded media", len(keys), err)
			}
			if existingVideo {
				if data, err := os.ReadFile(video); err != nil || string(data) != "existing video" {
					t.Fatal("upgrade changed existing video", err)
				}
			}
		})
	}
}

func TestV9StatusAndCatalogErrorsAreCurrentOnly(t *testing.T) {
	c := testConsole(t)
	good := monitorFull("kiva", "20261002000000", "good", strings.Repeat("a", 32))
	if usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{good}); !usable || err != nil {
		t.Fatal("could not establish existing catalog", usable, err)
	}
	// Valid IDs can pass network sampling while full business parsing fails.
	// Without a usable response, the current error must accompany retained data.
	bad := monitorNames("20261003000000", "invalid")
	bad.Body = bytes.Replace(bad.Body, []byte(`"id":1`), []byte(`"id":1,"playerCount":"wrong"`), 1)
	usable, err := c.syncCatalogs(t.Context(), []upstreamstate.CatalogResponse{bad})
	if usable || err == nil || c.metadata.CatalogError() == "" {
		t.Fatal("failed check lost its error", usable, err)
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://"+c.address+"/api/status", nil))
	var status map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err, w.Code)
	}
	if _, exists := status["traffic"]; exists {
		t.Fatal("retired traffic projection remains")
	}
	var current catalogSyncStatus
	if err := json.Unmarshal(status["catalogSync"], &current); err != nil || current.Initializing || !strings.Contains(current.Error, "playerCount") {
		t.Fatal("existing catalog error missing from status", current, err)
	}
	c = restartTestConsole(t, c)
	c.mu.Lock()
	syncStatus := c.catalogSyncSnapshotLocked()
	c.mu.Unlock()
	if syncStatus.Error != "" || c.localInventory().Error != "" {
		t.Fatal("restored previous catalog error", syncStatus)
	}
	if c.localInventory().Catalog.Revision != "20261002000000" {
		t.Fatal("accepted version lost")
	}
}
