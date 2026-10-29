package cacheproxy

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrImportSourceChanged = errors.New("源文件已移动、删除或修改")
var ErrImportedSourceMissing = errors.New("源文件已不存在，无需清理")

// ImportFile freezes a source's identity without reading its video contents.
type ImportFile struct {
	Source   string `json:"source"`
	FileID   string `json:"fileID"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// ImportedVideo is a durable receipt for exactly one newly published source.
type ImportedVideo struct {
	ImportFile
	Destination string `json:"destination"`
	MD5         string `json:"md5"`
}

func SnapshotImportFile(source string) (ImportFile, error) {
	f, info, err := openImportSource(source)
	if err != nil {
		return ImportFile{}, err
	}
	defer f.Close()
	id, err := fileIdentity(f)
	if err != nil {
		return ImportFile{}, err
	}
	if id == "" {
		return ImportFile{}, errors.New("无法确认源文件身份")
	}
	return ImportFile{Source: source, FileID: id, Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

func openImportSource(source string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil, nil, ErrImportSourceChanged
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, ErrImportSourceChanged
	}
	f, err := os.Open(source)
	if os.IsNotExist(err) {
		return nil, nil, ErrImportSourceChanged
	}
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err == nil && !sameVerifiedFile(info, opened) {
		err = ErrImportSourceChanged
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, opened, nil
}

func (snapshot ImportFile) check(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	id, err := fileIdentity(f)
	if err != nil {
		return err
	}
	if snapshot.FileID == "" || id != snapshot.FileID || !info.Mode().IsRegular() || info.Size() != snapshot.Size || info.ModTime().UnixNano() != snapshot.Modified {
		return ErrImportSourceChanged
	}
	return nil
}

func hashImport(ctx context.Context, f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := md5.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ImportVideo never overwrites an existing cache entry. A link attempt detects
// actual volume boundaries, including mount points, rather than drive letters.
func (s *Server) ImportVideo(ctx context.Context, source string, allowed map[string]bool) (*ImportedVideo, error) {
	snapshot, err := SnapshotImportFile(source)
	if err != nil {
		return nil, err
	}
	return s.ImportSnapshotVideo(ctx, snapshot, allowed)
}

// ImportSnapshotVideo never admits a replacement for the enumerated source.
func (s *Server) ImportSnapshotVideo(ctx context.Context, snapshot ImportFile, allowed map[string]bool) (*ImportedVideo, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	source := snapshot.Source
	f, info, err := openImportSource(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := snapshot.check(f); err != nil {
		return nil, err
	}
	key, err := hashImport(ctx, f)
	if err != nil {
		return nil, err
	}
	if err := snapshot.check(f); err != nil {
		return nil, err
	}
	if !allowed[key] {
		return nil, nil
	}
	s.pinVideo(video{key: key})
	defer s.releaseVideo(video{key: key})
	dest := s.cfg.videoFile(key)
	if _, err := os.Lstat(dest); err == nil {
		return nil, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	dir, err := os.MkdirTemp(s.cfg.publishDir(), "import-")
	if err != nil {
		return nil, err
	}
	stage := filepath.Join(dir, "video")
	defer func() {
		// Do not recursively delete unexpected files introduced into the stage.
		if err := os.Remove(stage); err != nil && !os.IsNotExist(err) {
			s.cfg.Logger.Warn("import_stage_cleanup_failed", "path", stage, "error", err)
		}
		if err := os.Remove(dir); err != nil {
			s.cfg.Logger.Warn("import_stage_cleanup_failed", "path", dir, "error", err)
		}
	}()
	if err = os.Link(source, stage); err != nil {
		if !importCrossDevice(err) {
			return nil, fmt.Errorf("创建硬链接失败：%w", err)
		}
		out, e := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Seek(0, io.SeekStart)
		if e == nil {
			_, e = io.Copy(out, contextReader{ctx, f})
		}
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e != nil {
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	current, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil, ErrImportSourceChanged
	}
	if err != nil {
		return nil, err
	}
	if !sameVerifiedFile(info, current) {
		return nil, ErrImportSourceChanged
	}
	staged, err := os.Open(stage)
	if err != nil {
		return nil, err
	}
	digest, err := hashImport(ctx, staged)
	staged.Close()
	if err != nil {
		return nil, err
	}
	current, err = os.Lstat(source)
	if os.IsNotExist(err) {
		return nil, ErrImportSourceChanged
	}
	if err != nil {
		return nil, err
	}
	if !sameVerifiedFile(info, current) {
		return nil, ErrImportSourceChanged
	}
	if err := snapshot.check(f); err != nil {
		return nil, err
	}
	if digest != key {
		return nil, errors.New("导入视频 MD5 校验失败")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	s.retentionRunMu.Lock()
	defer s.retentionRunMu.Unlock()
	policies, err := s.resourcePolicies(ctx)
	if err != nil {
		return nil, err
	}
	if policies[key].blocked {
		return nil, nil
	}
	if err = os.Link(stage, dest); err != nil {
		if os.IsExist(err) {
			return nil, nil
		}
		return nil, err
	}
	published, err := os.Lstat(dest)
	if err != nil {
		return nil, err
	}
	s.retentionMu.Lock()
	s.retainVideoLocked(key, &retainedVideo{path: dest, key: key, size: published.Size(), recent: published.ModTime().UnixMilli(), modified: published.ModTime().UnixNano()})
	s.retentionMu.Unlock()
	return &ImportedVideo{ImportFile: snapshot, Destination: dest, MD5: key}, nil
}

// RemoveImportedSource refuses stale receipts and missing/corrupt destinations.
func (s *Server) RemoveImportedSource(ctx context.Context, receipt ImportedVideo) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	if !validMD5(receipt.MD5) || filepath.Clean(receipt.Destination) != filepath.Clean(s.cfg.videoFile(receipt.MD5)) {
		return errors.New("导入回执不属于当前视频目录")
	}
	source, sourceInfo, err := openImportSource(receipt.Source)
	if errors.Is(err, ErrImportSourceChanged) {
		if _, statErr := os.Lstat(receipt.Source); os.IsNotExist(statErr) {
			return ErrImportedSourceMissing
		}
	}
	if err != nil {
		return err
	}
	defer source.Close()
	if err := receipt.ImportFile.check(source); err != nil {
		return err
	}
	s.pinVideo(video{key: receipt.MD5})
	defer s.releaseVideo(video{key: receipt.MD5})
	for _, path := range []string{receipt.Destination, receipt.Source} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("文件已被替换")
		}
		if path == receipt.Source && !sameVerifiedFile(sourceInfo, info) {
			return ErrImportSourceChanged
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		digest, err := hashImport(ctx, f)
		if err == nil && path == receipt.Source {
			err = receipt.ImportFile.check(f)
		}
		f.Close()
		if err != nil {
			return err
		}
		if digest != receipt.MD5 {
			if path == receipt.Source {
				return ErrImportSourceChanged
			}
			return errors.New("文件 MD5 已改变，未删除")
		}
	}
	// Windows does not permit unlinking a source while this handle is open.
	if err := source.Close(); err != nil {
		return err
	}
	info, err := os.Lstat(receipt.Source)
	if err != nil {
		return err
	}
	if !sameVerifiedFile(sourceInfo, info) {
		return ErrImportSourceChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(receipt.Source)
}
