//go:build !windows

package desktop

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

var processSession = rand.Text()

func canonicalExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }

// Non-Windows builds only expose their own headless console identity.
func processIdentity(pid uint32) (string, string, error) {
	if pid != uint32(os.Getpid()) {
		return "", "", fmt.Errorf("desktop discovery requires Windows")
	}
	exe, err := os.Executable()
	return exe, processSession, err
}
