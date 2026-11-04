package desktop

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyMenuTheme scopes the process-wide native menu preference to this popup.
// These uxtheme exports are undocumented; ordinal 135 had a different signature
// before Windows 10 1903. Leave the standard menu intact if unavailable.
func applyMenuTheme(light bool) func() {
	unchanged := func() {}
	version := windows.RtlGetVersion()
	if version.MajorVersion < 10 || version.MajorVersion == 10 && version.BuildNumber < 18362 {
		return unchanged
	}
	module, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return unchanged
	}
	setMode, setErr := windows.GetProcAddressByOrdinal(module, 135) // SetPreferredAppMode
	flush, flushErr := windows.GetProcAddressByOrdinal(module, 136) // FlushMenuThemes
	if setErr != nil || flushErr != nil {
		windows.FreeLibrary(module)
		return unchanged
	}

	var contrast struct {
		Size, Flags uint32
		Scheme      *uint16
	}
	contrast.Size = uint32(unsafe.Sizeof(contrast))
	// SPI_GETHIGHCONTRAST; keep the default mode if the query fails.
	ok, _, _ := user32.NewProc("SystemParametersInfoW").Call(0x42, uintptr(contrast.Size), uintptr(unsafe.Pointer(&contrast)), 0)
	mode := uintptr(0)
	if ok != 0 && contrast.Flags&1 == 0 {
		mode = 2 // ForceDark: also honor an explicit preference when Windows is light.
		if light {
			mode = 3 // ForceLight
		}
	}
	previous, _, _ := syscall.SyscallN(setMode, mode)
	syscall.SyscallN(flush)
	return func() {
		syscall.SyscallN(setMode, previous)
		syscall.SyscallN(flush)
		windows.FreeLibrary(module)
	}
}
