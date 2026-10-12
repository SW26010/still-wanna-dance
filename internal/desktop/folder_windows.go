//go:build windows

package desktop

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// SelectFolder opens the Windows folder dialog without a console window or UAC.
// Path/title are JSON on stdin, never executable PowerShell source. The helper
// process owns the dialog so request cancellation also closes its native UI.
func SelectFolder(ctx context.Context, title, initial string) (string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	input, err := json.Marshal(map[string]string{"title": title, "initial": initial})
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoProfile", "-NonInteractive", "-STA", "-Command", folderDialogScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("无法打开 Windows 文件夹选择器：%w", err)
	}
	var result struct{ Path string }
	if err = json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("无法读取所选文件夹：%w", err)
	}
	return result.Path, nil // Empty means the user canceled; preserve the draft.
}

// IFileDialog with FOS_PICKFOLDERS provides the modern Explorer-style picker.
// The shared script also drives the optional local UI preview.
//
//go:embed folder_dialog.ps1
var folderDialogScript string
