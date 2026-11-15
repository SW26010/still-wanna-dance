package cacheproxy

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func importLinkUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) ||
		errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION)
}

func importSameVolumeSpace(destination string) string {
	f, err := os.Open(destination)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(windows.Handle(f.Fd()), nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return "unknown"
	}
	if flags&windows.FILE_SUPPORTS_HARD_LINKS == 0 {
		return "copy-required"
	}
	return "same-volume"
}

// The stage is on the destination volume. Do not enable replacement or a
// copy/delete fallback: a concurrent destination must remain untouched.
func publishImportedVideo(stage, destination string) error {
	from, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "publish import", Old: stage, New: destination, Err: err}
	}
	return nil
}
