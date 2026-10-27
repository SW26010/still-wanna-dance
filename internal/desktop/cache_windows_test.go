package desktop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestOpenCacheLocationRejectsMissingFile(t *testing.T) {
	// Also exercise the S_FALSE branch on an already initialized COM thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(windows.S_FALSE) {
		t.Fatal(err)
	}
	defer windows.CoUninitialize()
	if err := OpenCacheLocation(filepath.Join(t.TempDir(), "missing.mp4"), true); err == nil || !strings.Contains(err.Error(), "无法解析本地文件") {
		t.Fatalf("expected a file parsing error after COM initialization, got %v", err)
	}
}

// Opt in because this test opens Explorer on the interactive desktop. Inspect
// the real shell window: successfully starting explorer.exe is not sufficient.
func TestOpenCacheLocationExplorer(t *testing.T) {
	if os.Getenv("SWD_EXPLORER_CHECK") != "1" {
		t.Skip("set SWD_EXPLORER_CHECK=1 to exercise the interactive Windows shell")
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		selectFile bool
	}{
		{"file", true},
		{"directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "曲库 videos, & files")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "0123456789abcdef0123456789abcdef.mp4")
			if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			target, selected := dir, ""
			if tc.selectFile {
				target, selected = file, file
			}
			if err := OpenCacheLocation(target, tc.selectFile); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-Command", explorerCheckScript)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			cmd.Env = append(os.Environ(), "SWD_EXPLORER_DIR="+dir, "SWD_EXPLORER_FILE="+selected)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Explorer visibility/selection: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}

const explorerCheckScript = `
$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class ExplorerCheck {
  [DllImport("user32.dll")]
  public static extern bool IsWindowVisible(IntPtr hwnd);
}
'@
$shell = New-Object -ComObject Shell.Application
$seen = $false
$visible = $false
$selected = $false
$lastError = ''
try {
  $deadline = [DateTime]::UtcNow.AddSeconds(5)
  do {
    foreach ($window in @($shell.Windows())) {
      try {
        if ($window.Document.Folder.Self.Path -ne $env:SWD_EXPLORER_DIR) { continue }
        $seen = $true
        $visible = [ExplorerCheck]::IsWindowVisible([IntPtr]$window.HWND)
        $selected = @($window.Document.SelectedItems() | ForEach-Object { $_.Path }) -contains $env:SWD_EXPLORER_FILE
        if ($visible -and (-not $env:SWD_EXPLORER_FILE -or $selected)) {
          Write-Output "PASS visible=$visible selected=$selected"
          exit 0
        }
      } catch { $lastError = $_.Exception.Message }
    }
    Start-Sleep -Milliseconds 100
  } while ([DateTime]::UtcNow -lt $deadline)
  throw "windowFound=$seen visible=$visible selected=$selected lastError=$lastError"
} finally {
  # Only close windows for this test's unique temporary directory.
  foreach ($window in @($shell.Windows())) {
    try {
      if ($window.Document.Folder.Self.Path -eq $env:SWD_EXPLORER_DIR) { $window.Quit() }
    } catch { }
  }
}
`
