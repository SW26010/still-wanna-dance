package desktop

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Verify the real process preference and its restoration without displaying UI
// or depending on the timing of Windows' popup fade animation.
func TestTrayNativeMenuThemeScope(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	version := windows.RtlGetVersion()
	if version.MajorVersion < 10 || version.MajorVersion == 10 && version.BuildNumber < 18362 {
		t.Skip("native menu theme overrides require Windows 10 1903")
	}
	module, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.FreeLibrary(module)
	setMode, setErr := windows.GetProcAddressByOrdinal(module, 135)
	flush, flushErr := windows.GetProcAddressByOrdinal(module, 136)
	if setErr != nil || flushErr != nil {
		t.Skip("native menu theme exports unavailable", setErr, flushErr)
	}
	var contrast struct {
		Size, Flags uint32
		Scheme      *uint16
	}
	contrast.Size = uint32(unsafe.Sizeof(contrast))
	ok, _, _ := user32.NewProc("SystemParametersInfoW").Call(0x42, uintptr(contrast.Size), uintptr(unsafe.Pointer(&contrast)), 0)
	if ok == 0 || contrast.Flags&1 != 0 {
		t.Skip("explicit colors yield to Windows high contrast")
	}
	// Start with a non-default preference to detect incorrect blanket resets.
	previous, _, _ := syscall.SyscallN(setMode, 3)
	defer func() {
		syscall.SyscallN(setMode, previous)
		syscall.SyscallN(flush)
	}()
	for _, preference := range []string{"dark", "light", "dark", "system", "light"} {
		want := uintptr(2)
		if usesLightTheme(preference) {
			want = 3
		}
		restore := applyMenuTheme(usesLightTheme(preference))
		applied, _, _ := syscall.SyscallN(setMode, want)
		restore()
		restored, _, _ := syscall.SyscallN(setMode, 3)
		if applied != want || restored != 3 {
			t.Fatalf("%s: applied=%d want=%d restored=%d want=3", preference, applied, want, restored)
		}
	}
}
