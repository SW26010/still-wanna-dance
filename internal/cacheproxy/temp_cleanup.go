package cacheproxy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
)

type mediaWorkKind int

const (
	blockWork mediaWorkKind = iota
	publicationWork
)

// The caller owns this directory's lease and has not started any writers.
// Each location recognizes only the file kinds it actually creates.
func cleanMediaWorkFiles(dir string, logger *slog.Logger, kind mediaWorkKind) (cleaned int, entryErr, dirErr error) {
	if err := workfiles.CheckDir(dir); err != nil {
		return 0, nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, nil, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return 0, nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return 0, nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		partial := kind == blockWork && workfiles.RandomName(name, "download-block-", ".part") ||
			kind == publicationWork && workfiles.RandomName(name, "download-publish-", ".part")
		importDir := kind == publicationWork && workfiles.RandomName(name, "import-", "")
		if !partial && !importDir {
			continue
		}
		removed, err := cleanMediaWorkEntry(root, name, partial, logger)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			if entryErr == nil {
				entryErr = fmt.Errorf("%s：%w", name, err)
			}
			logger.Warn("暂存文件清理失败，已跳过", "event_name", "temporary_cleanup_failed", "path", filepath.Join(dir, name), "error", err)
			continue
		}
		if removed {
			cleaned++
			logger.Info("暂存文件已清理", "event_name", "temporary_cleanup_removed", "path", filepath.Join(dir, name))
		}
	}
	return cleaned, entryErr, nil
}

func cleanMediaWorkEntry(root *os.Root, name string, partial bool, logger *slog.Logger) (bool, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return false, err
	}
	if partial && workfiles.Regular(info) {
		err := workfiles.RemoveFile(root, name)
		return err == nil, err
	}
	if partial || !workfiles.Directory(info) {
		logger.Warn("已保留无法确认的暂存路径", "event_name", "temporary_cleanup_skipped", "path", filepath.Join(root.Name(), name), "reason", "unexpected file type")
		return false, nil
	}
	// An import owns exactly one optional regular file called video.
	// Inspect the entire directory before deleting anything. Never RemoveAll.
	childDir, err := root.Open(name)
	if err != nil {
		return false, err
	}
	children, err := childDir.ReadDir(-1)
	childDir.Close()
	if err != nil {
		return false, err
	}
	if len(children) > 1 || (len(children) == 1 && children[0].Name() != "video") {
		logger.Warn("已保留无法确认的暂存路径", "event_name", "temporary_cleanup_skipped", "path", filepath.Join(root.Name(), name), "reason", "unknown import contents")
		return false, nil
	}
	if len(children) == 1 {
		child := filepath.Join(name, "video")
		childInfo, err := root.Lstat(child)
		if err != nil {
			return false, err
		}
		if !workfiles.Regular(childInfo) {
			logger.Warn("已保留无法确认的暂存路径", "event_name", "temporary_cleanup_skipped", "path", filepath.Join(root.Name(), child), "reason", "non-regular import file")
			return false, nil
		}
		if err := workfiles.RemoveFile(root, child); err != nil {
			return false, err
		}
	}
	err = root.Remove(name)
	return err == nil, err
}
