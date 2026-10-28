package console

import (
	"os"

	"golang.org/x/sys/windows"
)

func openHostsFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Check replacement permission as well as the existing write permission.
	// Preparation must not report recovery ready when only truncation is allowed.
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open hosts", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func preserveHostsPermissions(original, stage *os.File) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(original.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	// os.CreateTemp's handle lacks WRITE_DAC. Open the security descriptor by
	// name while that handle still prevents replacement of the staging entry.
	return windows.SetNamedSecurityInfo(stage.Name(), windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil)
}

func commitHostsFile(stage, path string) error {
	from, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// Both files are in the same directory. No cross-volume copy/delete fallback,
	// no prior removal of hosts; permissions were copied before this final rename.
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "replace hosts", Old: stage, New: path, Err: err}
	}
	return nil
}
