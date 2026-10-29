package cacheproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEstimateImportSpaceReadOnly(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	file := filepath.Join(source, "video.mp4")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(target, "missing")
	for _, tc := range []struct{ source, target, want string }{
		{source, target, "same-volume"},
		{source, source, "same-volume"},
		{source, missing, "unknown"},
		{missing, target, "unknown"},
		{file, target, "unknown"},
		{source, file, "unknown"},
		{"", target, "unknown"},
	} {
		if got := EstimateImportSpace(tc.source, tc.target); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "unchanged" {
		t.Fatal("estimate changed source", err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatal("estimate created destination files", entries, err)
	}
}
