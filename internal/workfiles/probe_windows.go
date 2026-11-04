package workfiles

import (
	"crypto/rand"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ProbeCreateDir checks the directory-specific right without leaving a
// temporary directory on a shared disk if the process is interrupted.
func ProbeCreateDir(dir string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	// FILE_APPEND_DATA is FILE_ADD_SUBDIRECTORY when opening a directory.
	h, err := windows.CreateFile(p, windows.FILE_APPEND_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// Probe uses kernel-managed deletion, including process termination. The path
// may be an external, shared directory: no startup scanner owns its contents.
func Probe(dir string) error {
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, ".swd-probe-"+rand.Text()))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE|windows.DELETE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}
