//go:build windows

package desktop

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
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
	cmd.Stdin = strings.NewReader(`{"title":"test","initial":""}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dialog definitions: %v: %s", err, output)
	}
	return string(output)
}
