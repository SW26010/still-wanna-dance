package workfiles

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func redirected(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&os.ModeSymlink != 0 || (ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

func removeFile(root *os.Root, name string, expected os.FileInfo) error {
	p, err := windows.UTF16PtrFromString(pathInRoot(root, name))
	if err != nil {
		return err
	}
	// No sharing: an open download/import (even through a hard link) prevents
	// deletion. OPEN_REPARSE_POINT opens the entry, never its link target.
	h, err := windows.CreateFile(p, windows.DELETE|windows.FILE_READ_ATTRIBUTES, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return err
	}
	if !Regular(actual) || !os.SameFile(expected, actual) {
		return fmt.Errorf("work file changed: %s", name)
	}
	// Delete by the verified handle, without reopening or truncating a path.
	disposition := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &disposition, uint32(unsafe.Sizeof(disposition)))
}
