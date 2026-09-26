//go:build linux || darwin

package cacheproxy

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", nil
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
