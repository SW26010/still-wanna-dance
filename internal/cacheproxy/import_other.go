//go:build !windows

package cacheproxy

import (
	"errors"
	"syscall"
)

func importCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }
