// Package workfiles handles disposable files inside an exclusively owned directory.
// Callers must hold the configuration/storage lease before cleaning, and must not
// run cleanup concurrently with writers. Unknown entries are never traversed.
package workfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckDir rejects redirected work directories, including Windows junctions.
func CheckDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || redirected(info) {
		return fmt.Errorf("unsafe work directory: %s", path)
	}
	return nil
}

// Regular deliberately uses Lstat: a link is never a disposable file.
func Regular(info os.FileInfo) bool { return info.Mode().IsRegular() && !redirected(info) }

// RandomName recognizes only the decimal suffix emitted by os.CreateTemp and
// os.MkdirTemp, rather than treating every file with a familiar prefix as ours.
func RandomName(name, prefix, suffix string) bool {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if len(n) == 0 || len(n) > 10 {
		return false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// RemoveFile only unlinks a regular file; hard-linked content is never changed.
// The Root confines removal even if a directory is concurrently redirected.
func RemoveFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !Regular(info) {
		return fmt.Errorf("unsafe work file: %s", name)
	}
	return removeFile(root, name, info)
}

func pathInRoot(root *os.Root, name string) string { return filepath.Join(root.Name(), name) }
