package cacheproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"still-wanna-dance/internal/workfiles"
)

// LibraryPaths identifies independently selected resources. It is not a stored
// identity or a binding between a database and a particular video directory.
type LibraryPaths struct {
	DatabasePath string
	VideoDir     string
}

// Resolve normalizes the selected resources and rejects reserved video paths.
func (p LibraryPaths) Resolve() (LibraryPaths, error) {
	if strings.TrimSpace(p.DatabasePath) == "" {
		return p, errors.New("请选择数据库文件")
	}
	var err error
	p.DatabasePath, err = filepath.Abs(p.DatabasePath)
	if err == nil && p.VideoDir != "" {
		p.VideoDir, err = filepath.Abs(p.VideoDir)
	}
	if err == nil && p.VideoDir != "" {
		if pathWithin(filepath.Join(p.VideoDir, ".swd-tmp"), p.DatabasePath) ||
			pathIdentity(p.DatabasePath) == pathIdentity(filepath.Join(p.VideoDir, ".swd.lock")) ||
			pathIdentity(filepath.Dir(p.DatabasePath)) == pathIdentity(p.VideoDir) && cacheVideoName.MatchString(filepath.Base(pathIdentity(p.DatabasePath))) {
			err = errors.New("数据库位置与视频文件或发布暂存范围冲突，请选择独立的数据库文件位置")
		}
	}
	return p, err
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(pathIdentity(parent), pathIdentity(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func pathIdentity(path string) string {
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// CacheStorageID scopes page selections to both resources. A changed database
// must invalidate an old delete stamp even when it describes the same file.
func CacheStorageID(paths LibraryPaths) string {
	h := sha256.Sum256([]byte(pathIdentity(paths.DatabasePath) + "\x00" + pathIdentity(paths.VideoDir)))
	return hex.EncodeToString(h[:])
}

func DatabaseID(path string) string {
	h := sha256.Sum256([]byte(pathIdentity(path)))
	return hex.EncodeToString(h[:])
}

// CacheDirectory validates the selected directory itself; it never appends a
// child name or creates a missing root. Read-only library views can report this
// error separately from database availability.
func CacheDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("尚未选择完整视频目录")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := workfiles.CheckDir(path); err != nil {
		return "", err
	}
	return path, nil
}

func (c Config) libraryPaths() LibraryPaths {
	return LibraryPaths{DatabasePath: c.Store.path, VideoDir: c.VideoDir}
}
