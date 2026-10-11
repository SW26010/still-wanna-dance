//go:build !windows

package workfiles

import "os"

func redirected(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

func removeFile(root *os.Root, name string, expected os.FileInfo) error {
	return root.Remove(name)
}
