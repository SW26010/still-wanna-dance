package cacheproxy

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func publishImportedVideo(stage, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		// Preserve no-replace publication on older kernels/filesystems.
		return os.Link(stage, destination)
	}
	if err != nil {
		return &os.LinkError{Op: "publish import", Old: stage, New: destination, Err: err}
	}
	return nil
}
