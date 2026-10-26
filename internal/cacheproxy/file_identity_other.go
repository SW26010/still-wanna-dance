//go:build !windows && !linux && !darwin

package cacheproxy

import (
	"fmt"
	"os"
)

// Without a stable file identity, always use full validation.
func fileIdentity(*os.File) (string, error) { return "", nil }

func fileLinkCount(*os.File) (uint64, error) {
	return 0, fmt.Errorf("file link count is unavailable on this platform")
}
