package desktop

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestProcessDefaultDPIAwareness(t *testing.T) {
	context, _, err := user32.NewProc("GetDpiAwarenessContextForProcess").Call(^uintptr(0))
	if context == 0 {
		t.Fatal(err)
	}
	if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(context, perMonitorV2); equal == 0 {
		t.Fatal("embedded manifest did not select Per-Monitor V2")
	}
}

func TestTaskDialogDPIContextRestoredOnFailure(t *testing.T) {
	// An MTA caller deliberately makes STA initialization fail before any UI.
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
			done <- err
			return
		}
		defer windows.CoUninitialize()
		previous, _, err := user32.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(0))
		if previous == 0 {
			done <- err
			return
		}
		defer user32.NewProc("SetThreadDpiAwarenessContext").Call(previous)
		if err := showTaskDialog(errors.New("test failure")); err == nil {
			done <- errors.New("MTA caller unexpectedly opened an STA dialog")
			return
		}
		current, _, _ := user32.NewProc("GetThreadDpiAwarenessContext").Call()
		if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(current, ^uintptr(0)); equal == 0 {
			done <- errors.New("failed dialog did not restore unaware context")
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type dpiRect struct{ Left, Top, Right, Bottom int32 }

func checkDialogMonitors(t *testing.T, hwnd uintptr) {
	t.Helper()
	// Physical coordinates are needed even if another test temporarily changed
	// a worker thread's DPI awareness.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restore, err := enterPerMonitorDPI()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	type monitor struct {
		handle uintptr
		work   dpiRect
		dpi    uint32
	}
	var monitors []monitor
	enumerate := syscall.NewCallback(func(handle, _, _, _ uintptr) uintptr {
		var info struct {
			Size         uint32
			Bounds, Work dpiRect
			Flags        uint32
		}
		info.Size = uint32(unsafe.Sizeof(info))
		var scale uint32
		ok, _, _ := user32.NewProc("GetMonitorInfoW").Call(handle, uintptr(unsafe.Pointer(&info)))
		hr, _, _ := windows.NewLazySystemDLL("shcore.dll").NewProc("GetScaleFactorForMonitor").Call(handle, uintptr(unsafe.Pointer(&scale)))
		if ok != 0 && int32(hr) >= 0 && scale != 0 {
			monitors = append(monitors, monitor{handle, info.Work, 96 * scale / 100})
		}
		return 1
	})
	if ok, _, err := user32.NewProc("EnumDisplayMonitors").Call(0, 0, enumerate, 0); ok == 0 || len(monitors) == 0 {
		t.Fatalf("could not enumerate display DPI: %v", err)
	}
	distinct := make(map[uint32]bool)
	var previousDPI uintptr
	var previousRect dpiRect
	// Return to the first monitor too, so a mixed-DPI setup checks both directions.
	for _, monitor := range append(monitors, monitors[0]) {
		distinct[monitor.dpi] = true
		var rect dpiRect
		user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		x := monitor.work.Left + (monitor.work.Right-monitor.work.Left-(rect.Right-rect.Left))/2
		y := monitor.work.Top + (monitor.work.Bottom-monitor.work.Top-(rect.Bottom-rect.Top))/2
		if ok, _, err := user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, 0x15); ok == 0 { // NOSIZE | NOZORDER | NOACTIVATE
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		var dpi uintptr
		for {
			dpi, _, _ = user32.NewProc("GetDpiForWindow").Call(hwnd)
			if dpi == uintptr(monitor.dpi) || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		t.Logf("monitor=%x dpi=%d expected=%d window=%+v", monitor.handle, dpi, monitor.dpi, rect)
		if dpi != uintptr(monitor.dpi) {
			t.Errorf("TaskDialog did not adopt its monitor DPI")
		}
		if previousDPI != 0 && dpi != previousDPI && rect.Right-rect.Left == previousRect.Right-previousRect.Left && rect.Bottom-rect.Top == previousRect.Bottom-previousRect.Top {
			t.Error("TaskDialog did not resize after a monitor DPI change")
		}
		previousDPI, previousRect = dpi, rect
		if rect.Left < monitor.work.Left || rect.Top < monitor.work.Top || rect.Right > monitor.work.Right || rect.Bottom > monitor.work.Bottom {
			t.Error("TaskDialog extends outside the monitor work area")
		}
		var pid uint32
		user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		reader := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", filepath.Join("..", "..", "scripts", "read-dialog-text.ps1"), "-ProcessId", fmt.Sprint(pid), "-CheckBounds")
		if output, err := reader.CombinedOutput(); err != nil {
			t.Errorf("dialog content bounds: %v\n%s", err, output)
		}
	}
	if len(distinct) < 2 {
		t.Log("Mixed-DPI transition NOT VERIFIED: no displays with distinct scaling available")
	}
}
