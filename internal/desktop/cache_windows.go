package desktop

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Explorer selects a file without executing it. Paths come from the cache engine.
func OpenCacheLocation(path string, selectFile bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if !selectFile {
		return windows.ShellExecute(0, wide("open"), name, nil, nil, windows.SW_SHOWNORMAL)
	}

	// Shell item APIs preserve file selection without Explorer command-line
	// parsing. COM initialization and cleanup must stay on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// S_FALSE means COM was already initialized; it still needs balancing.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
		return err
	}
	defer windows.CoUninitialize()
	var item unsafe.Pointer
	hr, _, _ := shell32.NewProc("SHParseDisplayName").Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&item)), 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("无法解析本地文件 (0x%08x)", uint32(hr))
	}
	defer windows.CoTaskMemFree(item)
	// With no child array, the absolute item identifies the file to select.
	hr, _, _ = shell32.NewProc("SHOpenFolderAndSelectItems").Call(uintptr(item), 0, 0, 0)
	if int32(hr) < 0 {
		return fmt.Errorf("无法定位本地文件 (0x%08x)", uint32(hr))
	}
	return nil
}
