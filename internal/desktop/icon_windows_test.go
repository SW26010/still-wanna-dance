package desktop

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// Exercise Windows' actual icon decoder without registering a tray icon or
// starting the application. Includes PNG frames and a size between ICO frames.
func TestBrandIconWindowsDecode(t *testing.T) {
	gdi32 := syscall.NewLazyDLL("gdi32.dll")
	for _, light := range []bool{false, true} {
		for _, size := range []int{16, 20, 24, 32, 36, 40, 48, 64, 128, 256} {
			t.Run(fmt.Sprintf("light=%t/%d", light, size), func(t *testing.T) {
				icon, err := makeIconAtSize(size, light)
				if err != nil {
					t.Fatal(err)
				}
				defer user32.NewProc("DestroyIcon").Call(icon)
				var info struct {
					IsIcon, XHotspot, YHotspot uint32
					Mask, Color                uintptr
				}
				if ok, _, err := user32.NewProc("GetIconInfo").Call(icon, uintptr(unsafe.Pointer(&info))); ok == 0 {
					t.Fatal(err)
				}
				defer gdi32.NewProc("DeleteObject").Call(info.Mask)
				defer gdi32.NewProc("DeleteObject").Call(info.Color)
				var bitmap struct {
					Type, Width, Height, WidthBytes int32
					Planes, BitsPixel               uint16
					Bits                            uintptr
				}
				if n, _, err := gdi32.NewProc("GetObjectW").Call(info.Color, unsafe.Sizeof(bitmap), uintptr(unsafe.Pointer(&bitmap))); n == 0 {
					t.Fatal(err)
				}
				if bitmap.Width != int32(size) || bitmap.Height != int32(size) || bitmap.BitsPixel != 32 {
					t.Fatalf("wrong icon bitmap: %+v", bitmap)
				}
			})
		}
	}
}

func TestTrayIconThemeSelection(t *testing.T) {
	tray := themeTestTray(t)
	for _, light := range []bool{false, true, true, false} {
		if err := tray.updateIcon(light); err != nil {
			t.Fatal(err)
		}
		want := tray.darkIcon
		if light {
			want = tray.lightIcon
		}
		if tray.icon != want {
			t.Fatal("theme selected the wrong icon")
		}
	}
	// An unsuccessful shell update must keep the previously applied theme.
	tray.added = true // No shell icon has been registered for this test.
	if err := tray.updateIcon(true); err == nil || tray.icon != tray.darkIcon {
		t.Fatal("failed shell update changed the applied icon")
	}
}

func themeTestTray(t *testing.T) *tray {
	t.Helper()
	dark, err := makeIconAtSize(32, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { user32.NewProc("DestroyIcon").Call(dark) })
	light, err := makeIconAtSize(32, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { user32.NewProc("DestroyIcon").Call(light) })
	return &tray{darkIcon: dark, lightIcon: light, options: Options{State: func() State { return State{} }}}
}

// Opt in on an interactive Windows desktop: register a temporary icon, exercise
// the real Shell API, and remove it. Never change Windows theme settings.
func TestTrayThemeShellIntegration(t *testing.T) {
	if os.Getenv("SWD_TEST_TRAY") != "1" {
		t.Skip("set SWD_TEST_TRAY=1 to exercise the interactive Windows shell")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tray := themeTestTray(t)
	hwnd, _, err := user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(wide("STATIC"))), uintptr(unsafe.Pointer(wide("SWD theme test"))), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer user32.NewProc("DestroyWindow").Call(hwnd)
	tray.hwnd = hwnd
	if err := tray.addIcon(); err != nil {
		t.Fatal(err)
	}
	defer tray.removeIcon()
	for _, light := range []bool{false, true, true, false} {
		if err := tray.updateIcon(light); err != nil {
			t.Fatal(err)
		}
	}
	want := tray.darkIcon
	if taskbarUsesLightTheme() {
		want = tray.lightIcon
	}
	for _, notification := range []uint32{wmSettingChange, wmThemeChanged} {
		if err := tray.updateIcon(!taskbarUsesLightTheme()); err != nil {
			t.Fatal(err)
		}
		tray.dispatch(hwnd, notification, 0, 0)
		if tray.icon != want {
			t.Fatal("theme notification did not restore the system preference")
		}
	}
	// Exercise the Explorer recovery path without restarting the user's shell.
	tray.removeIcon()
	tray.taskbar = 0xc001
	tray.dispatch(hwnd, tray.taskbar, 0, 0)
	if !tray.added || tray.icon != want {
		t.Fatal("recreated tray lost its theme")
	}
}
