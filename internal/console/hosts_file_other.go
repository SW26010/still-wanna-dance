//go:build !windows

package console

import "os"

func openHostsFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}

func preserveHostsPermissions(original, stage *os.File) error {
	info, err := original.Stat()
	if err != nil {
		return err
	}
	return stage.Chmod(info.Mode().Perm())
}

func commitHostsFile(stage, path string) error { return os.Rename(stage, path) }
