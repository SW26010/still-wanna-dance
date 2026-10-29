package console

import (
	"path/filepath"
	"testing"
)

func TestSettingsStorageRoot(t *testing.T) {
	if _, err := normalizeSettings(Settings{}); err == nil {
		t.Fatal("empty storage root accepted")
	}
	s := testConsole(t).settings
	setTestLibraryPaths(&s, "data")
	s, err := normalizeSettings(s)
	if err != nil || !filepath.IsAbs(testStorageRoot(s)) {
		t.Fatal(s, err)
	}
}
