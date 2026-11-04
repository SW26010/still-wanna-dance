package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/workfiles"
)

func defaultBlockCacheDir(executable string) string {
	return filepath.Join(filepath.Dir(executable), "work")
}

func blockWorkPath(executable, config string, s Settings) string {
	return apppaths.WorkForCacheRoot(executable, config, s.BlockCacheDir)
}

// workspaceDataPaths are resources that disposable work must never contain or
// be contained by. Include default library locations before first-run setup.
func workspaceDataPaths(config string, s Settings) []string {
	p := apppaths.ForConfig(config)
	database, video := s.DatabasePath, s.VideoDir
	if database == "" {
		database = filepath.Join(filepath.Dir(config), p.DefaultDatabase)
	}
	if video == "" {
		video = filepath.Join(filepath.Dir(config), p.DefaultVideo)
	}
	return []string{config, p.Lock, p.StateDir, p.LogDir, database, video}
}

func workspaceOverlaps(work string, paths []string) error {
	for _, path := range paths {
		if path != "" && (pathContains(work, path) || pathContains(path, work)) {
			return fmt.Errorf("临时缓存工作目录不能与程序数据或完整视频目录重叠：%s", path)
		}
	}
	return nil
}

// WorkLocationInspection separates an unavailable data resource from a cache
// failure. Its unresolved paths must be checked before deleting old blocks.
type WorkLocationInspection struct {
	Unresolved error
}

// InspectWorkLocation checks every available alias, even when another resource
// cannot be resolved. The caller must defer cleanup while Unresolved is non-nil.
func InspectWorkLocation(executable, config string, s Settings) (WorkLocationInspection, error) {
	var result WorkLocationInspection
	work := blockWorkPath(executable, config, s)
	paths := workspaceDataPaths(config, s)
	if err := workspaceOverlaps(work, paths); err != nil {
		return result, err
	}
	if s.BlockCacheDir != "" {
		if err := workfiles.CheckDir(s.BlockCacheDir); err != nil {
			return result, err
		}
	}
	if err := workfiles.CheckFutureDir(work); err != nil {
		return result, err
	}
	canonicalWork, err := desktop.CanonicalPath(work)
	if err != nil {
		return result, err
	}
	for i, path := range paths {
		canonical, err := desktop.CanonicalPath(path)
		if err != nil {
			if i < 4 {
				return result, err
			}
			name := "数据库"
			if i == 5 {
				name = "完整视频目录"
			}
			result.Unresolved = errors.Join(result.Unresolved, fmt.Errorf("无法核验%s（%s）：%w", name, path, err))
			continue
		}
		if err := workspaceOverlaps(canonicalWork, []string{canonical}); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Selecting a cache directory does not require unrelated data disks to be online.
func ValidateWorkLocation(executable, config string, s Settings) error {
	_, err := InspectWorkLocation(executable, config, s)
	return err
}

const pendingBlockCacheCleanupKey = "pendingBlockCacheCleanup"

func decodePendingBlockCacheCleanup(fields map[string]json.RawMessage) ([]string, error) {
	var roots []string
	if raw := fields[pendingBlockCacheCleanupKey]; raw != nil {
		if err := json.Unmarshal(raw, &roots); err != nil {
			return nil, fmt.Errorf("读取待清理缓存位置：%w", err)
		}
	}
	for _, root := range roots {
		if root != "" && !filepath.IsAbs(root) {
			return nil, fmt.Errorf("待清理缓存位置不是绝对路径")
		}
	}
	return roots, nil
}

// CleanupPendingBlockCaches only visits workspaces recorded when this config
// changed location. Offline or unsafe locations stay pending for a later run.
// The caller owns the configuration lease and has cleaned its atomic work slot.
func CleanupPendingBlockCaches(executable, config string, s Settings, current *cacheproxy.WorkDirectory, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	_, fields, err := readConfigFile(config)
	if err != nil {
		log.Warn("previous_block_cache_cleanup_deferred", "error", err)
		return
	}
	roots, err := decodePendingBlockCacheCleanup(fields)
	if err != nil {
		log.Warn("previous_block_cache_cleanup_deferred", "error", err)
		return
	}
	var completed []string
	for _, root := range roots {
		previous := s
		previous.BlockCacheDir = root
		path := blockWorkPath(executable, config, previous)
		if current != nil && sameResourcePath(path, current.Path()) {
			if !current.CleanupPending() {
				completed = append(completed, root) // Opening the current workspace already cleaned it.
			}
			continue
		}
		inspection, err := InspectWorkLocation(executable, config, previous)
		if err != nil {
			log.Warn("previous_block_cache_cleanup_deferred", "path", path, "error", err)
			continue
		}
		if inspection.Unresolved != nil {
			log.Warn("previous_block_cache_cleanup_deferred", "path", path, "error", inspection.Unresolved)
			continue
		}
		err = cacheproxy.CleanExistingWorkDirectory(path, log)
		if os.IsNotExist(err) {
			// If a selected disk disappeared during the check, retain the record.
			if root != "" && workfiles.CheckDir(root) != nil {
				continue
			}
			err = nil
		}
		if err != nil {
			log.Warn("previous_block_cache_cleanup_deferred", "path", path, "error", err)
			continue
		}
		completed = append(completed, root)
	}
	if len(completed) == 0 {
		return
	}
	if err := updateConfigFile(config, func(fields map[string]json.RawMessage, exists bool) error {
		if !exists {
			return os.ErrNotExist
		}
		pending, err := decodePendingBlockCacheCleanup(fields)
		if err != nil {
			return err
		}
		remaining := pending[:0]
		for _, root := range pending {
			done := false
			for _, old := range completed {
				if sameResourcePath(root, old) {
					done = true
					break
				}
			}
			if !done {
				remaining = append(remaining, root)
			}
		}
		if len(remaining) == 0 {
			delete(fields, pendingBlockCacheCleanupKey)
			return nil
		}
		fields[pendingBlockCacheCleanupKey], err = json.Marshal(remaining)
		return err
	}); err != nil {
		log.Warn("previous_block_cache_cleanup_record_failed", "error", err)
	}
}

func (c *Console) validateBlockCacheSelection(s Settings) error {
	if s.BlockCacheDir == "" {
		return nil
	}
	work := blockWorkPath(c.identity.Executable, c.configPath, s)
	if err := workspaceOverlaps(work, workspaceDataPaths(c.configPath, s)); err != nil {
		return err
	}
	c.mu.Lock()
	active := c.settings
	c.mu.Unlock()
	return workspaceOverlaps(work, []string{active.DatabasePath, active.VideoDir})
}

func (c *Console) prepareBlockCacheSelection(s, old Settings) error {
	if s.BlockCacheDir == "" || sameResourcePath(s.BlockCacheDir, old.BlockCacheDir) {
		return nil
	}
	if err := ValidateWorkLocation(c.identity.Executable, c.configPath, s); err != nil {
		return fmt.Errorf("临时缓存目录不可用：%w", err)
	}
	if err := workfiles.Probe(s.BlockCacheDir); err != nil {
		return fmt.Errorf("临时缓存目录不可写：%w", err)
	}
	work := blockWorkPath(c.identity.Executable, c.configPath, s)
	// Probe the workspace if it exists, otherwise its nearest existing parent.
	// Do not create a workspace while merely saving a selection.
	probe := s.BlockCacheDir
	for _, path := range []string{work, filepath.Dir(work)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("临时缓存目录不可用：%w", err)
		}
		if !workfiles.Directory(info) {
			return fmt.Errorf("临时缓存目录不可用：%s 不是安全目录", path)
		}
		probe = path
		break
	}
	if !sameResourcePath(probe, s.BlockCacheDir) {
		if err := workfiles.Probe(probe); err != nil {
			return fmt.Errorf("临时缓存目录不可写：%w", err)
		}
	}
	if !sameResourcePath(probe, work) {
		if err := workfiles.ProbeCreateDir(probe); err != nil {
			return fmt.Errorf("临时缓存目录不可写：%w", err)
		}
	}
	return nil
}
