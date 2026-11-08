package desktop

import (
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
)

// Owns only the child's dialog; no desktop mutex, tray, user configuration or
// browser is needed, so this also runs while the user's SWD is active.
func TestWindowsTaskDialog(t *testing.T) {
	if kind := os.Getenv("SWD_TASK_DIALOG_CHILD"); kind != "" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if kind == "unaware" {
			previous, _, err := user32.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(0))
			if previous == 0 {
				t.Fatal(err)
			}
			defer user32.NewProc("SetThreadDpiAwarenessContext").Call(previous)
		}
		before, _, _ := user32.NewProc("GetThreadDpiAwarenessContext").Call()
		cause := errors.New("读取程序配置：测试配置无效")
		if strings.HasPrefix(kind, "conflict") {
			config := `C:\Users\Example\Applications\still-wanna-dance\config.json`
			if kind == "conflict-custom" {
				config = `C:\Users\Example\Applications\still-wanna-dance\custom.json`
			}
			cause = fmt.Errorf("startup: %w", &InstanceConflictError{Existing: Identity{
				Executable: `C:\Users\Example\Applications\still-wanna-dance\SWD.exe`, Config: config, PID: 123,
			}})
		}
		if err := showTaskDialog(cause); err != nil {
			t.Fatal(err)
		}
		after, _, _ := user32.NewProc("GetThreadDpiAwarenessContext").Call()
		if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(before, after); equal == 0 {
			t.Fatal("TaskDialog did not restore its caller's DPI context")
		}
		return
	}
	if os.Getenv("SWD_TASK_DIALOG_TEST") != "1" {
		t.Skip("set SWD_TASK_DIALOG_TEST=1 for isolated native dialog verification")
	}
	for _, kind := range []string{"conflict", "conflict-custom", "error", "unaware"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsTaskDialog$", "-test.v")
			cmd.Env = append(os.Environ(), "SWD_TASK_DIALOG_CHILD="+kind)
			output := &strings.Builder{}
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			var dialog uintptr
			find := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
				var pid uint32
				user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
				if pid == uint32(cmd.Process.Pid) && dialogClass(hwnd) == "#32770" {
					dialog = hwnd
				}
				return 1
			})
			deadline := time.Now().Add(10 * time.Second)
			for dialog == 0 && time.Now().Before(deadline) {
				select {
				case err := <-done:
					t.Fatalf("dialog process exited before showing UI: %v\n%s", err, output)
				default:
				}
				user32.NewProc("EnumWindows").Call(find, 0)
				if dialog == 0 {
					time.Sleep(25 * time.Millisecond)
				}
			}
			if dialog == 0 {
				t.Fatal("TaskDialog did not appear")
			}
			var taskUI bool
			findTaskUI := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
				taskUI = taskUI || dialogClass(hwnd) == "DirectUIHWND"
				return 1
			})
			user32.NewProc("EnumChildWindows").Call(dialog, findTaskUI, 0)
			if !taskUI {
				t.Error("dialog did not use the Common Controls task dialog UI")
			}
			awareness, _, _ := user32.NewProc("GetWindowDpiAwarenessContext").Call(dialog)
			if equal, _, _ := user32.NewProc("AreDpiAwarenessContextsEqual").Call(awareness, perMonitorV2); equal == 0 {
				t.Error("TaskDialog window is not Per-Monitor V2")
			}
			reader := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", filepath.Join("..", "..", "scripts", "read-dialog-text.ps1"), "-ProcessId", fmt.Sprint(cmd.Process.Pid))
			text, err := reader.CombinedOutput()
			if err != nil {
				t.Fatalf("read TaskDialog: %v\n%s", err, text)
			}
			want := []string{"无法完成操作", "读取程序配置：测试配置无效"}
			if strings.HasPrefix(kind, "conflict") {
				want = []string{instanceConflictInstruction, instanceConflictExplanation, `C:\Users\Example\Applications\still-wanna-dance\SWD.exe`, "123"}
				if kind == "conflict-custom" {
					want = append(want, `C:\Users\Example\Applications\still-wanna-dance\custom.json`)
				} else if strings.Contains(string(text), "配置：") || strings.Contains(string(text), `C:\Users\Example\Applications\still-wanna-dance\config.json`) {
					t.Errorf("TaskDialog repeated the default configuration path: %s", text)
				}
			}
			for _, expected := range want {
				if !strings.Contains(string(text), expected) {
					t.Errorf("TaskDialog lost %q: %s", expected, text)
				}
			}
			if kind == "conflict" {
				checkDialogMonitors(t, dialog)
			}
			// TDM_CLICK_BUTTON / IDCLOSE, targeted only at the child.
			if ok, _, err := user32.NewProc("PostMessageW").Call(dialog, 0x400+102, 8, 0); ok == 0 {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("TaskDialog failed: %v\n%s", err, output)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("dialog process remained after Close")
			}
		})
	}
}

func dialogClass(hwnd uintptr) string {
	var buffer [64]uint16
	user32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return syscall.UTF16ToString(buffer[:])
}
