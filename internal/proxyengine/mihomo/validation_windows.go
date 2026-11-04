package mihomo

import (
	"os/exec"
	"syscall"
)

func hideValidationWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
