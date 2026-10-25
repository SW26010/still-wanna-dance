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

// SelectDatabase opens the Windows file dialog without a console window or UAC.
// Path/title are JSON on stdin; request cancellation closes the helper-owned UI.
func SelectDatabase(ctx context.Context, title, initial string) (string, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	input, err := json.Marshal(map[string]any{"title": title, "initial": initial})
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
		return "", fmt.Errorf("无法打开 Windows 路径选择器：%w", err)
	}
	var result struct{ Path string }
	if err = json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("无法读取所选路径：%w", err)
	}
	return result.Path, nil // Empty means the user canceled; preserve the draft.
}

// IFileDialog provides the modern Explorer-style database picker.
// The shared script also drives the optional local UI preview.
//
//go:embed folder_dialog.ps1
var folderDialogScript string
