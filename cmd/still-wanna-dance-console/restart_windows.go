package main

import (
	"os/exec"
	"syscall"
)

func configureRestartCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
