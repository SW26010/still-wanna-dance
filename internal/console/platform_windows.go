package console

import (
	"fmt"
	"os"
	"os/exec"
	"still-wanna-dance/internal/legal"
	"strings"
	"syscall"
)

func changeHosts(action string) error {
	if action != "enable" && action != "disable" {
		return fmt.Errorf("invalid action")
	}
	b, err := os.ReadFile(hostsPath())
	if err != nil {
		return err
	}
	next, err := transformHosts(string(b), action)
	if err != nil {
		return err
	}
	if next == string(b) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := "$p = Start-Process -FilePath '" + strings.ReplaceAll(exe, "'", "''") + "' -ArgumentList '-hosts-action', '" + action + "', '-accept-terms', '" + legal.Version + "' -Verb RunAs -WindowStyle Hidden -Wait -PassThru; exit $p.ExitCode"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hosts 修改未完成（可能取消了 UAC）：%s %w", out, err)
	}
	return nil
}

func FlushDNS() {
	cmd := exec.Command("ipconfig.exe", "/flushdns")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
