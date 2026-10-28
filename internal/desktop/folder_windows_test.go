//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"still-wanna-dance/internal/branding"
)

// Compile the embedded COM definitions with the actual Windows PowerShell
// runtime, without displaying a dialog or reading any user's selected folder.
func TestFolderDialogDefinitionLoads(t *testing.T) {
	output := runFolderDialogProbe(t, "[Console]::Out.Write([SWDFolderDialog].FullName)")
	if output != "SWDFolderDialog" {
		t.Fatalf("unexpected result: %s", output)
	}
}

// Exercise the real user32 API on the same STA thread as the picker. In
// particular, an exception must restore the helper's original DPI context.
func TestFolderDialogDPIContext(t *testing.T) {
	output := runFolderDialogProbe(t, `
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class DpiProbe {
  [DllImport("user32.dll")]
  public static extern IntPtr GetThreadDpiAwarenessContext();
  [DllImport("user32.dll")]
  public static extern bool AreDpiAwarenessContextsEqual(IntPtr a, IntPtr b);
}
'@
$before = [DpiProbe]::GetThreadDpiAwarenessContext()
$scopeType = [SWDFolderDialog].GetNestedType('DpiScope', [Reflection.BindingFlags]::NonPublic)
$scope = [Activator]::CreateInstance($scopeType, $true)
try {
  $during = [DpiProbe]::GetThreadDpiAwarenessContext()
  if (-not [DpiProbe]::AreDpiAwarenessContextsEqual($during, [IntPtr]::new(-4))) {
    throw 'folder dialog is not per-monitor-v2 aware'
  }
  try { throw 'simulated dialog failure' } catch { } finally { $scope.Dispose() }
  $after = [DpiProbe]::GetThreadDpiAwarenessContext()
  if (-not [DpiProbe]::AreDpiAwarenessContextsEqual($before, $after)) {
    throw 'folder dialog did not restore DPI context'
  }
  [Console]::Out.Write('PMv2/restored')
} finally { $scope.Dispose() }
`)
	if output != "PMv2/restored" {
		t.Fatalf("unexpected DPI probe: %s", output)
	}
}

// Opt-in desktop check: inspect the actual common dialog's caption icon, then
// close only this test dialog from its own STA message loop. No user input needed.
func TestPathDialogNative(t *testing.T) {
	if os.Getenv("SWD_PATH_PICKER_EVIDENCE") == "" {
		t.Skip("set SWD_PATH_PICKER_EVIDENCE for native path and icon checks")
	}
	dir := t.TempDir()
	existing := filepath.Join(dir, "已有 资料.sqlite")
	if err := os.WriteFile(existing, []byte("original fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, initial string
		accept        bool
	}{
		{"database", existing, true},
		{"database", filepath.Join(dir, "新建 资料.sqlite"), true},
		{"open", existing, true},
		{"folder", dir, true},
		{"database", existing, false},
	} {
		t.Run(tc.mode+"/"+filepath.Base(tc.initial)+"/"+strconv.FormatBool(tc.accept), func(t *testing.T) {
			selected := runNativePathProbe(t, tc.mode, tc.initial, tc.accept)
			want := ""
			if tc.accept {
				want = tc.initial
			}
			if selected != want {
				t.Fatalf("selected %q, want %q", selected, want)
			}
		})
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "original fixture" {
		t.Fatal("selection modified existing file", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "新建 资料.sqlite")); !os.IsNotExist(err) {
		t.Fatal("native selection created a file", err)
	}
}

func runNativePathProbe(t *testing.T, mode, initial string, accept bool) string {
	t.Helper()
	probe := `
Add-Type -ReferencedAssemblies System.Windows.Forms, System.Drawing -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Drawing;
using System.Runtime.InteropServices;
using System.Windows.Forms;
public static class NativeIconProbe {
  delegate bool WindowVisitor(IntPtr window, IntPtr data);
  [DllImport("kernel32.dll")] static extern uint GetCurrentThreadId();
  [DllImport("user32.dll")] static extern bool EnumThreadWindows(uint thread, WindowVisitor visitor, IntPtr data);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetWindowText(IntPtr window, StringBuilder text, int max);
  [DllImport("user32.dll")] static extern IntPtr SendMessage(IntPtr window, uint message, IntPtr wparam, IntPtr lparam);
  static Timer timer;
  static byte[] iconBytes;
  static string result;
  static bool accept;
  static int attempts;
  public static void Start(string iconBase64, bool select) {
    accept = select;
    iconBytes = Convert.FromBase64String(iconBase64);
    result = "dialog not observed";
    attempts = 0;
    timer = new Timer(); timer.Interval = 300;
    timer.Tick += delegate {
      EnumThreadWindows(GetCurrentThreadId(), delegate(IntPtr window, IntPtr data) {
        var title = new StringBuilder(256); GetWindowText(window, title, 256);
        if (title.ToString() != "SWD icon verification") return true;
        try {
          IntPtr handle = SendMessage(window, 0x7f, IntPtr.Zero, IntPtr.Zero); // WM_GETICON / ICON_SMALL
          if (handle == IntPtr.Zero) handle = SendMessage(window, 0x7f, new IntPtr(2), IntPtr.Zero);
          if (handle == IntPtr.Zero) throw new Exception("dialog has no caption icon");
          using (var actualIcon = Icon.FromHandle(handle))
          using (var actual = actualIcon.ToBitmap())
          using (var stream = new MemoryStream(iconBytes))
          using (var expectedIcon = new Icon(stream, actual.Size))
          using (var expected = expectedIcon.ToBitmap()) {
            if (actual.Size != expected.Size) throw new Exception("icon size mismatch");
            for (int x = 0; x < actual.Width; x++) for (int y = 0; y < actual.Height; y++)
              if (actual.GetPixel(x,y) != expected.GetPixel(x,y)) throw new Exception("dialog caption does not use SWD artwork");
          }
          result = "brand-icon/verified";
        } catch (Exception error) { result = error.Message; }
        // A titled HWND can be enumerated before Shell finishes initializing it.
        // Bound the readiness wait; a persistent wrong/missing icon still fails.
        if (result != "brand-icon/verified" && ++attempts < 10) return false;
        timer.Stop();
        bool confirm = accept && result == "brand-icon/verified";
        SendMessage(window, confirm ? 0x111u : 0x10u, confirm ? new IntPtr(1) : IntPtr.Zero, IntPtr.Zero); // IDOK or WM_CLOSE
        return false;
      }, IntPtr.Zero);
    };
    timer.Start();
  }
  public static string Finish() { timer.Dispose(); return result; }
}
'@
$icon = $request.icon
[NativeIconProbe]::Start($icon, ACCEPT_SELECTION)
$selected = [SWDFolderDialog]::Select('SWD icon verification', 'INITIAL_PATH', 'DIALOG_MODE', $icon)
$probeResult = [NativeIconProbe]::Finish()
if ($probeResult -ne 'brand-icon/verified') { throw $probeResult }
[Console]::Out.Write($selected)
`
	probe = strings.ReplaceAll(probe, "INITIAL_PATH", strings.ReplaceAll(initial, "'", "''"))
	probe = strings.ReplaceAll(probe, "DIALOG_MODE", mode)
	probe = strings.ReplaceAll(probe, "ACCEPT_SELECTION", "$"+strconv.FormatBool(accept))
	return runFolderDialogProbe(t, probe)
}

func runFolderDialogProbe(t *testing.T, probe string) string {
	t.Helper()
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	definition, _, found := strings.Cut(folderDialogScript, "$selected = [SWDFolderDialog]::Select")
	if !found {
		t.Fatal("missing dialog entry point")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-STA", "-Command", definition+"\n"+probe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	input, err := json.Marshal(map[string]any{"title": "test", "initial": "", "icon": branding.ICO})
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dialog definitions: %v: %s", err, output)
	}
	return string(output)
}
