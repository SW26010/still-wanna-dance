package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Returned by run after its shutdown sequence; main starts the replacement only
// after run's deferred instance/config leases and log writer have been closed.
type restartRequest struct{ args []string }

func (*restartRequest) Error() string { return "application restart requested" }

func restartApplication(args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	configureRestartCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("重新启动应用失败，请手动启动：%w", err)
	}
	return cmd.Process.Release()
}
