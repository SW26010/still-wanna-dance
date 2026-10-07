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

// ImportedVideo is a receipt for exactly one newly published source file.
type ImportedVideo struct {
	Source, Destination, MD5 string
	info                     os.FileInfo
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
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("源视频不是普通文件")
	}
	f, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !sameVerifiedFile(info, opened) {
		return nil, errors.New("源视频已改变")
	}
	key, err := hashImport(ctx, f)
	if err != nil {
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
	dir, err := os.MkdirTemp(s.cfg.tempDir(), "import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	stage := filepath.Join(dir, "video")
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
	if err != nil {
		return nil, err
	}
	if !sameVerifiedFile(info, current) {
		return nil, errors.New("源视频在导入期间改变")
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
	if digest != key {
		return nil, errors.New("导入视频 MD5 校验失败")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
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
	return &ImportedVideo{Source: source, Destination: dest, MD5: key, info: info}, nil
}

// RemoveImportedSource refuses stale receipts and missing/corrupt destinations.
func (s *Server) RemoveImportedSource(ctx context.Context, receipt ImportedVideo) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
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
		if path == receipt.Source && !sameVerifiedFile(receipt.info, info) {
			return errors.New("源文件已改变，未删除")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		digest, err := hashImport(ctx, f)
		f.Close()
		if err != nil {
			return err
		}
		if digest != receipt.MD5 {
			return errors.New("文件 MD5 已改变，未删除")
		}
	}
	info, err := os.Lstat(receipt.Source)
	if err != nil {
		return err
	}
	if !sameVerifiedFile(receipt.info, info) {
		return errors.New("源文件已改变，未删除")
	}
	return os.Remove(receipt.Source)
}
