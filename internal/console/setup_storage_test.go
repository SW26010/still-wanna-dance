package console

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetupStorageInspectionDoesNotInitializeOrSave(t *testing.T) {
	c := testConsole(t)
	before, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "not-created", "media")
	result, err := c.inspectSetupStorage(dir)
	if err != nil || result.Writable.State != "ok" || result.Library.State != "ok" {
		t.Fatalf("inspection: %+v %v", result, err)
	}
	if runtime.GOOS == "windows" && (result.Space.State != "ok" || result.TotalBytes == 0 || result.FreeBytes > result.TotalBytes) {
		t.Fatalf("missing or inconsistent partition capacity: %+v", result)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Fatal("inspection created media parent", err)
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) {
		t.Fatal("inspection saved configuration")
	}
}

func TestSetupStorageDefaultAndExternalBoundary(t *testing.T) {
	root := t.TempDir()
	portable := filepath.Join(root, "Portable")
	if err := os.Mkdir(portable, 0700); err != nil {
		t.Fatal(err)
	}
	defaultPath := filepath.Join(portable, "data", "storage")
	for _, tc := range []struct {
		name, path      string
		custom, allowed bool
	}{
		{"default", defaultPath, false, true},
		{"manual default", defaultPath, true, false},
		{"portable root", portable, true, false},
		{"portable descendant", filepath.Join(portable, "new", "media"), true, false},
		{"ordinary save inside portable", filepath.Join(portable, "other"), false, false},
		{"sibling prefix", filepath.Join(root, "Portable-media"), true, true},
		{"outside", filepath.Join(root, "external", "media"), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStorageLocation(tc.path, defaultPath, portable, tc.custom)
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

func TestSetupStorageSaveRejectsPortableTargetBeforeConfigWrite(t *testing.T) {
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

func TestSetupStorageRejectsLegacyAndIncompleteLibraries(t *testing.T) {
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
			result, err := c.inspectSetupStorage(root)
			if err != nil || result.Library.State != "failed" {
				t.Fatalf("accepted broken library: %+v %v", result, err)
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

func TestSetupStorageRecognizesLostLibraryInExistingRoot(t *testing.T) {
	c := testConsole(t)
	c.settings.StorageDir = t.TempDir()
	c.inventory.Videos = 1
	result, err := c.inspectSetupStorage(c.settings.StorageDir)
	if err != nil || result.Library.State != "failed" {
		t.Fatalf("lost library advertised as new: %+v %v", result, err)
	}
	entries, err := os.ReadDir(c.settings.StorageDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("inspection changed lost library: %v %v", entries, err)
	}
}
