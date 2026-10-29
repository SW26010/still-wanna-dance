package console

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/workfiles"
)

func (c *Console) defaultDatabasePath() string {
	return filepath.Join(filepath.Dir(c.configPath), c.files().DefaultDatabase)
}

func (c *Console) defaultVideoDir() string {
	return filepath.Join(filepath.Dir(c.configPath), c.files().DefaultVideo)
}

func (c *Console) portableDir() string {
	return filepath.Dir(c.identity.Executable)
}

// Keep custom media outside the installation, including aliases and paths whose
// last components do not exist yet. The default library remains inside Portable.
func validateStorageLocation(path, defaultPath, portable string) error {
	if pathContains(path, defaultPath) && pathContains(defaultPath, path) {
		return nil
	}
	if portable == "" {
		return fmt.Errorf("无法确认程序文件夹，请重试")
	}
	root, err := desktop.CanonicalPath(portable)
	if err != nil {
		return fmt.Errorf("无法确认程序文件夹：%w", err)
	}
	target, err := desktop.CanonicalPath(path)
	if err != nil {
		return fmt.Errorf("无法确认存储目录：%w", err)
	}
	if pathContains(portable, path) || pathContains(root, target) {
		return fmt.Errorf("请选择程序文件夹之外的位置，或填写默认存储目录")
	}
	return nil
}

func sameResourcePath(a, b string) bool { return cacheproxy.DatabaseID(a) == cacheproxy.DatabaseID(b) }

// Pure path validation also runs when loading configuration. Filesystem access
// belongs to changed selections only; an offline video disk must not prevent
// saving unrelated settings or opening the business database.
func (c *Console) validateResourcePaths(s Settings) error {
	p := c.files()
	for _, path := range []string{s.DatabasePath, s.VideoDir} {
		if path == "" {
			continue
		}
		if err := apppaths.ValidateWorkSeparation(c.identity.Executable, path); err != nil {
			return err
		}
		for _, reserved := range []string{p.Config, p.Lock, p.Terms, p.ChannelKey} {
			if sameResourcePath(path, reserved) || sameResourcePath(path, filepath.Join(filepath.Dir(reserved), workfiles.TempName(reserved))) {
				return errors.New("数据位置不能覆盖程序配置或辅助文件")
			}
		}
		for _, reserved := range []string{p.StateDir, filepath.Dir(p.Log)} {
			if pathContains(reserved, path) {
				return errors.New("数据位置不能放在程序状态或日志目录内")
			}
		}
	}
	if s.VideoDir != "" && pathContains(s.VideoDir, p.Config) {
		return errors.New("完整视频目录不能包含程序配置")
	}
	if s.DatabasePath != "" {
		_, err := libraryPaths(s).Resolve()
		return err
	}
	return nil
}

// Read-only validation for the review before saving. No selected directory is
// created here, and no database is opened for writing or adopted by the app.
func (c *Console) inspectLibrarySelection(s, old Settings) (bool, error) {
	databaseChanged := !sameResourcePath(s.DatabasePath, old.DatabasePath)
	videoChanged := !sameResourcePath(s.VideoDir, old.VideoDir)
	if databaseChanged && s.DatabasePath != "" {
		if err := validateStorageLocation(s.DatabasePath, c.defaultDatabasePath(), c.portableDir()); err != nil {
			return false, err
		}
		if _, err := cacheproxy.LoadCatalogStatus(context.Background(), s.DatabasePath); err != nil {
			return false, fmt.Errorf("请选择已有的有效数据库，或先明确新建：%w", err)
		}
	}
	if videoChanged && s.VideoDir != "" {
		if err := validateStorageLocation(s.VideoDir, c.defaultVideoDir(), c.portableDir()); err != nil {
			return false, err
		}
		if err := workfiles.CheckFutureDir(s.VideoDir); err != nil {
			return false, err
		}
	}
	if s.VideoDir == "" || (!databaseChanged && !videoChanged) {
		return false, nil
	}
	entries, err := os.ReadDir(s.VideoDir)
	if err != nil {
		// Reusing an unavailable location with a different database still needs
		// a policy decision before the disk returns. Never recreate that root.
		if !videoChanged {
			return databaseChanged, nil
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && cacheName.MatchString(entry.Name()) {
			return true, nil
		}
	}
	return false, nil
}

// Acknowledgement belongs to the exact selection shown in the dialog, not to
// whichever settings happen to be saved when the user finishes reviewing it.
type videoPolicyConfirmation struct {
	DatabasePath     string `json:"databasePath"`
	VideoDir         string `json:"videoDir"`
	VideoStorageMode string `json:"videoStorageMode"`
	VideoBudgetBytes int64  `json:"videoBudgetBytes"`
	RetainFavorites  bool   `json:"retainFavorites"`
}

func (v videoPolicyConfirmation) matches(s Settings) bool {
	return sameResourcePath(v.DatabasePath, s.DatabasePath) && sameResourcePath(v.VideoDir, s.VideoDir) &&
		v.VideoStorageMode == s.VideoStorageMode && v.VideoBudgetBytes == s.VideoBudgetBytes && v.RetainFavorites == s.RetainFavorites
}

func (c *Console) prepareLibrarySelection(s, old Settings, confirmed *videoPolicyConfirmation) error {
	if confirmed != nil && !confirmed.matches(s) {
		return errors.New("目录或视频保留策略已变化，请重新确认后保存。")
	}
	required, err := c.inspectLibrarySelection(s, old)
	if err != nil {
		return err
	}
	if required && confirmed == nil {
		return errors.New("请先确认此视频目录将按当前数据库的收藏与视频保留策略管理，再保存设置")
	}
	if s.VideoDir != "" && !sameResourcePath(s.VideoDir, old.VideoDir) {
		if err := os.MkdirAll(s.VideoDir, 0700); err != nil {
			return err
		}
		if err := workfiles.CheckDir(s.VideoDir); err != nil {
			return err
		}
		return workfiles.Probe(s.VideoDir)
	}
	return nil
}
