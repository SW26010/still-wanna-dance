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
	s.StorageDir = "data"
	s, err := absoluteSettings(s)
	if err != nil || !filepath.IsAbs(s.StorageDir) {
		t.Fatal(s, err)
	}
}
