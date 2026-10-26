package console

import (
	"path/filepath"
	"testing"
)

func TestSettingsStorageRoot(t *testing.T) {
	if _, err := absoluteSettings(Settings{}); err == nil {
		t.Fatal("empty storage root accepted")
	}
	s := testConsole(t).settings
	setTestLibraryPaths(&s, "data")
	s, err := absoluteSettings(s)
	if err != nil || !filepath.IsAbs(testStorageRoot(s)) {
		t.Fatal(s, err)
	}
}
