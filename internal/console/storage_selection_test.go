package console

import (
	"os"
	"path/filepath"
	"testing"
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

func TestStorageSaveRejectsPortableTargetBeforeConfigWrite(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	saved := c.savedSettings
	changed := saved
	changed.StorageDir = filepath.Join(portableDir(), "new-media-not-created")
	if err := c.save(changed); err == nil {
		t.Fatal("saved custom Portable target")
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) || c.savedSettings != saved {
		t.Fatal("failed selection changed configuration")
	}
}

func TestStorageRejectsLegacyAndIncompleteLibraries(t *testing.T) {
	c := testConsole(t)
	for _, marker := range []string{"stepstash.sqlite", "videos", "storage.sqlite"} {
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
			_, err := c.inspectStorage(root)
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
	c.settings.StorageDir = t.TempDir()
	c.inventory.Videos = 1
	_, err := c.inspectStorage(c.settings.StorageDir)
	if err == nil {
		t.Fatalf("lost library advertised as new: %v", err)
	}
	entries, err := os.ReadDir(c.settings.StorageDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection changed lost library: %v %v", entries, err)
	}
}
