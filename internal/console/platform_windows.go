package console

import (
	"os/exec"
	"syscall"
)

func FlushDNS() {
	cmd := exec.Command("ipconfig.exe", "/flushdns")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
