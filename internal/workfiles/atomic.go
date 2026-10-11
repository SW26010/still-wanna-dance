package workfiles

import (
	"fmt"
	"os"
	"path/filepath"
)

// TempName reserves one sibling slot per destination, including when multiple
// custom configurations share a parent. There are no random names to accumulate.
func TempName(path string) string { return "." + filepath.Base(path) + ".swd-tmp" }

// Write replaces a file atomically. The caller serializes writes to this path.
// An existing work file is an error until the owner performs startup cleanup;
// it is never opened for truncation (it could be a link or belong to a writer).
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := CheckDir(dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	if info, err := root.Lstat(name); err == nil && !Regular(info) {
		return fmt.Errorf("unsafe destination: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp := TempName(path)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

// Clean removes exactly one known abandoned atomic-write slot, never the
// destination. Call only after acquiring its configuration lease and before
// creating any Console/Monitor. Missing parents do not need initialization.
func Clean(path string) (bool, error) {
	dir := filepath.Dir(path)
	if err := CheckDir(dir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	err = RemoveFile(root, TempName(path))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
