package console

import (
	"os"
	"path/filepath"
	"testing"

	"still-wanna-dance/internal/cacheproxy"
)

func createReaderLibrary(t *testing.T, c *Console) {
	t.Helper()
	if c.store != nil {
		return
	}
	paths := libraryPaths(c.settings)
	store, err := cacheproxy.CreateStore(paths.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.VideoDir, 0700); err != nil {
		t.Fatal(err)
	}
}

// Existing behavior fixtures intentionally use the familiar sibling layout;
// independent-location tests set the two fields directly.
func fixtureLibraryPaths(root string) cacheproxy.LibraryPaths {
	return cacheproxy.LibraryPaths{DatabasePath: filepath.Join(root, "storage.sqlite"), VideoDir: filepath.Join(root, "videos")}
}
func testStorageRoot(s Settings) string {
	if s.DatabasePath == "" {
		return ""
	}
	return filepath.Dir(s.DatabasePath)
}
func setTestLibraryPaths(s *Settings, root string) {
	paths := fixtureLibraryPaths(root)
	s.DatabasePath, s.VideoDir = paths.DatabasePath, paths.VideoDir
}

func createTestDatabase(t *testing.T, path string) {
	t.Helper()
	store, err := cacheproxy.CreateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
