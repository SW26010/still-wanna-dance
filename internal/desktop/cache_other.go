//go:build !windows

package desktop

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func OpenCacheLocation(path string, selectFile bool) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		args := []string{path}
		if selectFile {
			args = []string{"-R", path}
		}
		cmd = exec.Command("open", args...)
	} else {
		if selectFile {
			path = filepath.Dir(path)
		}
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
