package console

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func existingLibraryVideo(t *testing.T, c *Console, running bool) (string, string) {
	t.Helper()
	body := "existing library video"
	key := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	if err := c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Revision: "20261028000000", Source: cacheproxy.KivaCatalogURL, Songs: []cacheproxy.CatalogSong{{ID: 1, MD5: key}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.settings.VideoDir, key+".mp4")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if running {
		// An active reader keeps this existing video under the zero budget.
		_, release, err := c.retainEngineRead(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	return path, body
}

func TestRejectedImportPreservesExistingVideo(t *testing.T) {
	for _, running := range []bool{false, true} {
		for _, sourceKind := range []string{"root", "nested", "missing", "file"} {
			t.Run(fmt.Sprintf("engine=%t/%s", running, sourceKind), func(t *testing.T) {
				c := testConsole(t)
				path, body := existingLibraryVideo(t, c, running)
				engine := c.service
				source := c.settings.VideoDir
				switch sourceKind {
				case "nested":
					source = filepath.Join(source, "nested")
					if err := os.Mkdir(source, 0700); err != nil {
						t.Fatal(err)
					}
				case "missing":
					source = filepath.Join(t.TempDir(), "missing")
				case "file":
					source = path
				}
				if err := c.startImport(source); err == nil {
					t.Fatal("invalid source accepted")
				}
				if c.service != engine || c.importStatus != (ImportStatus{}) || c.importDone != nil {
					t.Error("rejected import started an engine or import task")
				}
				if got, err := os.ReadFile(path); err != nil || string(got) != body {
					t.Errorf("rejected import changed existing video: %q, %v", got, err)
				}
			})
		}
	}
}

func TestCatalogRefreshPreservesExistingVideo(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("engine=%t", running), func(t *testing.T) {
			c := testConsole(t)
			path, body := existingLibraryVideo(t, c, running)
			engine := c.service
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, testMD5CatalogBody("20261029000000", map[int]string{1: body}))
			}))
			t.Cleanup(api.Close)
			c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
			c.startInventoryScan()
			v := waitInventory(t, c)
			if c.service != engine {
				t.Error("catalog refresh started or replaced video engine")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != body {
				t.Errorf("catalog refresh changed existing video: %q, %v", got, err)
			}
			if v.Error != "" || !v.CoverageKnown || v.CoveredSongs != 1 || v.TotalSongs != 1 || v.CatalogRevision != "20261029000000" {
				t.Fatalf("unexpected coverage: %+v", v)
			}
		})
	}
}

func TestCatalogRefreshUpdatesMetadataWhenVideoDirectoryLost(t *testing.T) {
	c := testConsole(t)
	_, body := existingLibraryVideo(t, c, false)
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revision := "20261029000000"
		if requests.Add(1) > 1 {
			revision = "20261029000001"
		}
		fmt.Fprint(w, testMD5CatalogBody(revision, map[int]string{1: body}))
	}))
	t.Cleanup(api.Close)
	c.checksumURL, c.client.Transport = api.URL, http.DefaultTransport
	c.startInventoryScan()
	before := waitInventory(t, c)
	if before.Error != "" || !before.CoverageKnown {
		t.Fatal(before)
	}
	if c.service != nil {
		t.Fatal("metadata refresh unexpectedly started engine")
	}
	videoDir := c.settings.VideoDir
	if err := os.Rename(videoDir, videoDir+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	c.startInventoryScan()
	after := waitInventory(t, c)
	if requests.Load() != 2 || after.Catalog.Revision != "20261029000001" {
		t.Errorf("video directory loss blocked metadata update: requests=%d, %+v", requests.Load(), after)
	}
	if after.CoverageKnown || !strings.Contains(after.Error, videoDir) || after.Updated != before.Updated {
		t.Errorf("lost directory did not invalidate old coverage while retaining check time: %+v", after)
	}
	if c.service != nil {
		t.Error("lost directory started an engine")
	}
	if _, err := os.Stat(videoDir); !os.IsNotExist(err) {
		t.Error("lost directory recreated", err)
	}
	if err := c.store.Error(); err != nil {
		t.Error("video fault poisoned business store", err)
	}
}
