//go:build !windows

package cacheproxy

import (
	"errors"
	"syscall"
)

func importLinkUnavailable(err error) bool {
	// POSIX also uses EPERM when the filesystem does not support hard links.
	return errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM)
}

func importSameVolumeSpace(string) string { return "same-volume" }
