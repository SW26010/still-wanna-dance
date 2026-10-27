package console

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func TestStorageDefaultAndExternalBoundary(t *testing.T) {
	root := t.TempDir()
	portable := filepath.Join(root, "Portable")
	if err := os.Mkdir(portable, 0700); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(portable, "data", "storage")
	for _, tc := range []struct {
		name, path string
		allowed    bool
	}{
		{"default", defaultPath, true},
		{"portable root", portable, false},
		{"portable descendant", filepath.Join(portable, "new", "media"), false},
		{"sibling prefix", filepath.Join(root, "Portable-media"), true},
		{"outside", filepath.Join(root, "external", "media"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStorageLocation(tc.path, defaultPath, portable)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, err=%v", tc.allowed, err)
			}
		})
	}
	entries, err := os.ReadDir(portable)
	if err != nil || len(entries) != 0 {
		t.Fatalf("validation changed Portable: %v %v", entries, err)
	}
}

func TestStorageSelectionRejectsCustomPortableLocations(t *testing.T) {
	for _, scenario := range []string{"missing-database", "missing-videos", "invalid-database", "other-filename", "other-directory", "create-old-location"} {
		t.Run(scenario, func(t *testing.T) {
			c := testConsole(t)
			root := filepath.Dir(c.configPath)
			c.identity.Executable = filepath.Join(root, "SWD.exe")
			paths := fixtureLibraryPaths(filepath.Join(root, "data", "storage"))
			if scenario == "other-filename" {
				paths.DatabasePath = filepath.Join(filepath.Dir(paths.DatabasePath), "other.sqlite")
			}
			if scenario == "other-directory" {
				paths = fixtureLibraryPaths(filepath.Join(root, "unrelated"))
			}
			missingDatabase := scenario == "missing-database" || scenario == "create-old-location"
			if !missingDatabase {
				createTestDatabase(t, paths.DatabasePath)
			}
			if scenario == "invalid-database" {
				if err := os.WriteFile(paths.DatabasePath, []byte("damaged database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "missing-videos" {
				if err := os.MkdirAll(paths.VideoDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(paths.DatabasePath)
			configBefore, err := os.ReadFile(c.configPath)
			if err != nil {
				t.Fatal(err)
			}
			action := "check"
			if scenario == "create-old-location" {
				action = "create-database"
			}
			if w := storageRequest(t, c, action, paths); w.Code != 400 {
				t.Fatal("accepted invalid old selection", w.Code, w.Body.String())
			}
			if missingDatabase {
				if _, err := os.Lstat(paths.DatabasePath); !os.IsNotExist(err) {
					t.Fatal("created old database", err)
				}
			} else if after, err := os.ReadFile(paths.DatabasePath); err != nil || !bytes.Equal(before, after) {
				t.Fatal("modified rejected database", err)
			}
			if scenario == "missing-videos" {
				if _, err := os.Lstat(paths.VideoDir); !os.IsNotExist(err) {
					t.Fatal("recreated missing old videos", err)
				}
			}
			if after, err := os.ReadFile(c.configPath); err != nil || !bytes.Equal(configBefore, after) {
				t.Fatal("changed configuration", err)
			}
		})
	}
}

func TestStorageSaveRejectsPortableTargetBeforeConfigWrite(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	saved := c.savedSettings
	changed := saved
	setTestLibraryPaths(&changed, filepath.Join(c.portableDir(), "new-media-not-created"))
	if err := c.save(changed); err == nil {
		t.Fatal("saved custom Portable target")
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) || c.savedSettings != saved {
		t.Fatal("failed selection changed configuration")
	}
}

func TestStorageRejectsMissingAndInvalidDatabases(t *testing.T) {
	for _, marker := range []string{"unrelated.sqlite", "videos", "storage.sqlite"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, marker)
			if marker == "videos" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := cacheproxy.LoadCatalogStatus(context.Background(), fixtureLibraryPaths(root).DatabasePath)
			if err == nil {
				t.Fatalf("accepted broken library: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != marker {
				t.Fatal("changed original library", entries, err)
			}
			if marker != "videos" {
				b, _ := os.ReadFile(path)
				if string(b) != "invalid" {
					t.Fatal("modified marker")
				}
			}
		})
	}
}

func TestStorageRecognizesLostLibraryInExistingRoot(t *testing.T) {
	c := testConsole(t)
	setTestLibraryPaths(&c.settings, t.TempDir())
	c.inventory.Videos = 1
	_, err := cacheproxy.LoadCatalogStatus(context.Background(), libraryPaths(c.settings).DatabasePath)
	if err == nil {
		t.Fatalf("lost library advertised as new: %v", err)
	}
	entries, err := os.ReadDir(testStorageRoot(c.settings))
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection changed lost library: %v %v", entries, err)
	}
}
