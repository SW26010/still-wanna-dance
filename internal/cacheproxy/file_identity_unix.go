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

func fileLinkCount(f *os.File) (uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("file link count is unavailable")
	}
	return uint64(stat.Nlink), nil
}

func fileVolumeID(f *os.File) (uint64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("volume identity is unavailable")
	}
	return uint64(stat.Dev), nil
}
