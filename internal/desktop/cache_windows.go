package desktop

import (
	"os/exec"
	"syscall"
)

// Explorer selects a file without executing it. Paths come from the cache engine.
func OpenCacheLocation(path string, selectFile bool) error {
	args := []string{path}
	if selectFile {
		args = []string{"/select,", path}
	}
	cmd := exec.Command("explorer.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
