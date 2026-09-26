//go:build !windows && !linux && !darwin

package cacheproxy

import "os"

// Without a stable file identity, always use full validation.
func fileIdentity(*os.File) (string, error) { return "", nil }
