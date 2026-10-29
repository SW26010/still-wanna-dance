package cacheproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEstimateImportSpaceJunction(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	for _, tc := range []struct{ name, target, want string }{
		{"same", target, "same-volume"},
		{"cross", os.Getenv("SWD_IMPORT_TEST_VOLUME"), "cross-volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.target == "" {
				t.Skip("set SWD_IMPORT_TEST_VOLUME to read a second volume's directory")
			}
			link := filepath.Join(source, tc.name)
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, tc.target).CombinedOutput(); err != nil {
				t.Fatalf("junction: %v: %s", err, out)
			}
			t.Cleanup(func() {
				if err := os.Remove(link); err != nil {
					t.Error(err)
				}
			})
			// Both paths have the same textual drive letter, but the open
			// directory handle follows the junction to its actual volume.
			if got := EstimateImportSpace(link, target); got != tc.want {
				t.Fatalf("junction estimate = %s, want %s", got, tc.want)
			}
		})
	}
}
