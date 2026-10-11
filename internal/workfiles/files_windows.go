package workfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func redirected(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&os.ModeSymlink != 0 || (ok && data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

func removeFile(root *os.Root, name string, expected os.FileInfo) error {
	if !filepath.IsLocal(name) {
		return fmt.Errorf("invalid work filename: %s", name)
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	p, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	// No sharing: an open download/import (even through a hard link) prevents
	// deletion. OPEN_REPARSE_POINT opens the entry, never its link target.
	// Open relative to the pinned Root handle and reject reparse traversal in
	// every component, so a replaced parent cannot redirect deletion outside it.
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(dir.Fd()), ObjectName: p, Attributes: windows.OBJ_DONT_REPARSE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.DELETE|windows.FILE_READ_ATTRIBUTES, &oa, &status, nil, 0, 0, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
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
