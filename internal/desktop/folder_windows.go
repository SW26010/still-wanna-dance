//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// SelectPath opens the Windows file/directory dialog without a console window or UAC.
// Path/title are JSON on stdin; request cancellation closes the helper-owned UI.
func SelectPath(ctx context.Context, dialog PathDialog) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, exe, "-path-dialog")
	return runPathDialogCommand(ctx, cmd, dialog)
}

func runPathDialogCommand(ctx context.Context, cmd *exec.Cmd, dialog PathDialog) (string, error) {
	input, err := json.Marshal(dialog)
	if err != nil {
		return "", err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("无法打开 Windows 路径选择器：%w：%s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	var result pathDialogResult
	if err = json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("无法读取所选路径：%w", err)
	}
	return result.Path, nil // Empty means the user canceled; preserve the draft.
}
