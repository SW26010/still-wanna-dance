package cacheproxy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
)

// Called only after the storage lease and format checks, before any workers.
func cleanTemporaryFiles(dir string, logger *slog.Logger) (int, error) {
	if err := workfiles.CheckDir(dir); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, entry := range entries {
		name := entry.Name()
		partial := workfiles.RandomName(name, "download-", ".part") || workfiles.RandomName(name, "download-publish-", ".part")
		importDir := workfiles.RandomName(name, "import-", "")
		if !partial && !importDir {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			return cleaned, err
		}
		if partial && workfiles.Regular(info) {
			err = workfiles.RemoveFile(root, name)
		} else if importDir && info.IsDir() && workfiles.CheckDir(filepath.Join(dir, name)) == nil {
			// An import owns exactly one optional regular file called video.
			// Inspect the entire directory before deleting anything. Never RemoveAll.
			children, readErr := os.ReadDir(filepath.Join(dir, name))
			if readErr != nil {
				return cleaned, readErr
			}
			if len(children) > 1 || (len(children) == 1 && children[0].Name() != "video") {
				logger.Warn("temporary_cleanup_skipped", "path", filepath.Join(dir, name), "reason", "unknown import contents")
				continue
			}
			if len(children) == 1 {
				child := filepath.Join(name, "video")
				childInfo, statErr := root.Lstat(child)
				if statErr != nil {
					return cleaned, statErr
				}
				if !workfiles.Regular(childInfo) {
					logger.Warn("temporary_cleanup_skipped", "path", child, "reason", "non-regular import file")
					continue
				}
				err = workfiles.RemoveFile(root, child)
			}
			if err == nil {
				err = root.Remove(name)
			}
		} else {
			logger.Warn("temporary_cleanup_skipped", "path", filepath.Join(dir, name), "reason", "unexpected file type")
			continue
		}
		if err != nil {
			return cleaned, fmt.Errorf("remove %s: %w", name, err)
		}
		cleaned++
		logger.Info("temporary_cleanup_removed", "path", filepath.Join(dir, name))
	}
	return cleaned, nil
}
