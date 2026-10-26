package console

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestApplicationOwnsDatabaseBeforeEngineStarts(t *testing.T) {
	c := testConsole(t)
	path := libraryPaths(c.settings).DatabasePath
	if c.store == nil || c.service != nil {
		t.Fatal("database ownership must not depend on an engine", c.storeOpenError)
	}
	if other, err := cacheproxy.OpenStore(path); err == nil {
		other.Close()
		t.Fatal("a second writer opened the application's database")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := cacheproxy.OpenStore(path)
	if err != nil {
		t.Fatal("application close did not release the database", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationReportsDatabaseOpenFailureWithoutCreatingLibrary(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"schemaVersion":7,"databasePath":"missing/storage.sqlite","videoDir":"missing/videos"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := newConsole(config, "127.0.0.1:18081", newFixtureDNS(), testWorkDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	var status struct {
		StorageError string `json:"storageError"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != 200 || status.StorageError == "" {
		t.Fatal("database failure was not visible without an engine", w.Code, w.Body.String(), err)
	}
	if c.store != nil || c.service != nil {
		t.Fatal("missing database started a business store or engine")
	}
	paths := libraryPaths(c.settings)
	for _, path := range []string{paths.DatabasePath, paths.VideoDir} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("opening/status recreated an absent resource", path, err)
		}
	}
}
