package workfiles

import (
	"crypto/rand"
	"path/filepath"

	"golang.org/x/sys/windows"
)

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
