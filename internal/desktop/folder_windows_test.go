package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPathDialogHelperProcess(t *testing.T) {
	if os.Getenv("SWD_PATH_DIALOG_CHILD") != "1" {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Verify successful selection/cancellation also restores the caller's DPI.
	user32.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(0))
	if err := RunPathDialog(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	current, _, _ := user32.NewProc("GetThreadDpiAwarenessContext").Call()
	if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(current, ^uintptr(0)); equal == 0 {
		fmt.Fprintln(os.Stderr, "picker did not restore caller's DPI context")
		os.Exit(1)
	}
	os.Exit(0)
}

// Exercise the actual Shell configuration without displaying any window.
func TestPathDialogConfiguration(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"folder", "open", "database"} {
		t.Run(mode, func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
				t.Fatal(err)
			}
			defer windows.CoUninitialize()
			initial := filepath.Join(dir, "moved", "subdirectory")
			if mode != "folder" {
				initial = filepath.Join(initial, "已有 资料.sqlite")
			}
			dialog, err := newFileDialog(PathDialog{Mode: mode, Title: "选择资料", Initial: initial})
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.SyscallN(dialog.vtbl.Release, uintptr(unsafe.Pointer(dialog)))
			var options uint32
			if err := dialog.call(dialog.vtbl.GetOptions, uintptr(unsafe.Pointer(&options))); err != nil {
				t.Fatal(err)
			}
			common := uint32(fosForceFilesystem | fosPathMustExist | fosNoChangeDir | fosDontAddToRecent | fosNoTestFileCreate)
			if options&common != common || (options&fosPickFolders != 0) != (mode == "folder") || (mode == "database" && options&(fosFileMustExist|fosOverwritePrompt) != 0) || (mode == "open" && options&fosFileMustExist == 0) {
				t.Fatalf("wrong selection options: 0x%x", options)
			}
			var folder *shellItem
			if err := dialog.call(dialog.vtbl.GetFolder, uintptr(unsafe.Pointer(&folder))); err != nil {
				t.Fatal(err)
			}
			defer syscall.SyscallN(folder.vtbl.Release, uintptr(unsafe.Pointer(folder)))
			var path *uint16
			hr, _, _ := syscall.SyscallN(folder.vtbl.GetDisplayName, uintptr(unsafe.Pointer(folder)), 0x80058000, uintptr(unsafe.Pointer(&path)))
			if err := pathDialogHRESULT("GetDisplayName", hr); err != nil {
				t.Fatal(err)
			}
			defer windows.CoTaskMemFree(unsafe.Pointer(path))
			if got := windows.UTF16PtrToString(path); got != dir {
				t.Fatalf("initial folder %q, want nearest existing parent %q", got, dir)
			}
		})
	}
}

func TestPathDialogDPIContextRestoredOnFailure(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// An MTA forces STA initialization to fail before displaying any window.
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
		t.Fatal(err)
	}
	defer windows.CoUninitialize()
	previous, _, _ := user32.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(0))
	defer user32.NewProc("SetThreadDpiAwarenessContext").Call(previous)
	if _, err := showPathDialog(PathDialog{Mode: "folder"}); err == nil {
		t.Fatal("MTA caller unexpectedly opened an STA dialog")
	}
	current, _, _ := user32.NewProc("GetThreadDpiAwarenessContext").Call()
	if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(current, ^uintptr(0)); equal == 0 {
		t.Fatal("failed picker did not restore DPI context")
	}
}

func pathDialogTestCommand(ctx context.Context) *exec.Cmd {
	if exe := os.Getenv("SWD_PATH_PICKER_EXE"); exe != "" {
		return exec.CommandContext(ctx, exe, "-path-dialog")
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPathDialogHelperProcess$")
	cmd.Env = append(os.Environ(), "SWD_PATH_DIALOG_CHILD=1")
	return cmd
}

// The protocol/parent-cancellation path runs even without an interactive desktop.
func TestPathDialogCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runPathDialogCommand(ctx, pathDialogTestCommand(ctx), PathDialog{Mode: "folder"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if _, err := runPathDialogCommand(ctx, pathDialogTestCommand(ctx), PathDialog{Mode: "invalid"}); err == nil || !strings.Contains(err.Error(), "无效的路径选择模式") {
		t.Fatalf("lost helper failure: %v", err)
	}
}

// Opt-in real desktop checks. Every HWND is found by this child's PID and all
// dialogs are accepted/canceled automatically; no user data or app is opened.
func TestPathDialogNative(t *testing.T) {
	if os.Getenv("SWD_PATH_PICKER_EVIDENCE") == "" {
		t.Skip("set SWD_PATH_PICKER_EVIDENCE for native path, icon, DPI and cancellation checks")
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "已有 资料.sqlite")
	if err := os.WriteFile(existing, []byte("original fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, initial, action string
	}{
		{"database", existing, "accept"},
		{"database", filepath.Join(dir, "新建 资料.sqlite"), "accept"},
		{"open", existing, "accept"},
		{"folder", dir, "accept"},
		{"database", existing, "cancel"},
		{"folder", dir, "context"},
	} {
		t.Run(tc.mode+"/"+tc.action+"/"+filepath.Base(tc.initial), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := pathDialogTestCommand(ctx)
			request, _ := json.Marshal(PathDialog{Mode: tc.mode, Title: "SWD native picker verification", Initial: tc.initial})
			cmd.Stdin = bytes.NewReader(request)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			var output, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &stderr
			foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
			foregroundEnabled, _, _ := user32.NewProc("IsWindowEnabled").Call(foreground)
			started := time.Now()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var hwnd uintptr
			find := syscall.NewCallback(func(window, _ uintptr) uintptr {
				var pid uint32
				user32.NewProc("GetWindowThreadProcessId").Call(window, uintptr(unsafe.Pointer(&pid)))
				visible, _, _ := user32.NewProc("IsWindowVisible").Call(window)
				if pid == uint32(cmd.Process.Pid) && visible != 0 && dialogClass(window) == "#32770" {
					hwnd = window
				}
				return 1
			})
			for hwnd == 0 {
				select {
				case err := <-done:
					t.Fatalf("picker ended before visible: %v\n%s", err, &stderr)
				case <-ctx.Done():
					t.Fatal("picker did not appear")
				default:
				}
				user32.NewProc("EnumWindows").Call(find, 0)
				if hwnd == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			t.Logf("process-to-visible: %s", time.Since(started))
			awareness, _, _ := user32.NewProc("GetWindowDpiAwarenessContext").Call(hwnd)
			if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(awareness, perMonitorV2); equal == 0 {
				t.Error("picker is not Per-Monitor V2")
			}
			owner, _, _ := user32.NewProc("GetWindow").Call(hwnd, 4) // GW_OWNER
			var ownerPID uint32
			user32.NewProc("GetWindowThreadProcessId").Call(owner, uintptr(unsafe.Pointer(&ownerPID)))
			if owner == 0 || ownerPID != uint32(cmd.Process.Pid) {
				t.Error("picker has no local proxy owner")
			}
			if visible, _, _ := user32.NewProc("IsWindowVisible").Call(owner); visible != 0 {
				t.Error("proxy owner is visible")
			}
			// Querying the caption icon also waits for the Shell's UI thread.
			icon, _, _ := user32.NewProc("SendMessageW").Call(hwnd, 0x7f, 0, 0) // WM_GETICON / ICON_SMALL
			if icon == 0 {
				icon, _, _ = user32.NewProc("SendMessageW").Call(hwnd, 0x7f, 2, 0)
			}
			if icon == 0 {
				t.Error("picker has no caption icon")
			} else {
				checkPathDialogIcon(t, icon)
			}
			switch tc.action {
			case "accept":
				user32.NewProc("PostMessageW").Call(hwnd, wmCommand, 1, 0) // IDOK
			case "cancel":
				user32.NewProc("PostMessageW").Call(hwnd, wmClose, 0, 0)
			case "context":
				cancel()
			}
			err := <-done
			if tc.action == "context" {
				if err == nil || ctx.Err() == nil {
					t.Fatal("cancellation did not terminate helper", err)
				}
			} else {
				if err != nil {
					t.Fatalf("picker: %v\n%s", err, &stderr)
				}
				var result pathDialogResult
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatal(err, output.String())
				}
				want := ""
				if tc.action == "accept" {
					want = tc.initial
				}
				if result.Path != want {
					t.Fatalf("selected %q, want %q", result.Path, want)
				}
			}
			if valid, _, _ := user32.NewProc("IsWindow").Call(hwnd); valid != 0 {
				t.Error("picker survived helper exit")
			}
			if valid, _, _ := user32.NewProc("IsWindow").Call(owner); valid != 0 {
				t.Error("proxy survived helper exit")
			}
			if valid, _, _ := user32.NewProc("IsWindow").Call(foreground); valid != 0 && foregroundEnabled != 0 {
				if enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(foreground); enabled == 0 {
					t.Error("helper exit left the foreground window disabled")
				}
			}
		})
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "original fixture" {
		t.Fatal("selection modified existing file", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "新建 资料.sqlite")); !os.IsNotExist(err) {
		t.Fatal("selection created a file", err)
	}
}

func checkPathDialogIcon(t *testing.T, actual uintptr) {
	t.Helper()
	bits := func(icon uintptr) ([]byte, int) {
		t.Helper()
		var info struct {
			IsIcon, X, Y uint32
			Mask, Color  uintptr
		}
		if ok, _, err := user32.NewProc("GetIconInfo").Call(icon, uintptr(unsafe.Pointer(&info))); ok == 0 {
			t.Fatal(err)
		}
		gdi := windows.NewLazySystemDLL("gdi32.dll")
		defer gdi.NewProc("DeleteObject").Call(info.Mask)
		defer gdi.NewProc("DeleteObject").Call(info.Color)
		var bitmap struct {
			Type, Width, Height, WidthBytes int32
			Planes, BitsPixel               uint16
			Bits                            uintptr
		}
		if ok, _, err := gdi.NewProc("GetObjectW").Call(info.Color, unsafe.Sizeof(bitmap), uintptr(unsafe.Pointer(&bitmap))); ok == 0 {
			t.Fatal(err)
		}
		data := make([]byte, bitmap.WidthBytes*bitmap.Height)
		if n, _, err := gdi.NewProc("GetBitmapBits").Call(info.Color, uintptr(len(data)), uintptr(unsafe.Pointer(&data[0]))); int(n) != len(data) {
			t.Fatal("read icon pixels", err)
		}
		return data, int(bitmap.Width)
	}
	actualBits, size := bits(actual)
	expected, err := makeIconAtSize(size, false)
	if err != nil {
		t.Fatal(err)
	}
	defer user32.NewProc("DestroyIcon").Call(expected)
	expectedBits, _ := bits(expected)
	if !bytes.Equal(actualBits, expectedBits) {
		t.Error("picker caption does not use SWD artwork")
	}
}
