package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

// CheckLocal validates existing bytes only. It never starts an engine, downloads,
// publishes, deletes, or updates usage. A missing/corrupt video is a scan result;
// an unreadable file is an error and must not replace a successful snapshot.
func CheckLocal(ctx context.Context, storageDir, target string) (bool, error) {
	hit, _, err := CheckLocalReceipt(ctx, storageDir, target)
	return hit, err
}

// LocalReceipt remembers a successfully checked file for an immediate follow-up.
// It is process-local and invalidated by replacement, removal or metadata changes.
type LocalReceipt struct {
	path string
	info os.FileInfo
}

// ReuseLocalSong preserves the song association without hashing unchanged bytes.
func (s *Server) ReuseLocalSong(ctx context.Context, id, target string, receipt *LocalReceipt) (bool, error) {
	if receipt == nil {
		return false, nil
	}
	if !s.beginRequest() {
		return false, context.Canceled
	}
	defer s.wg.Done()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	v, err := s.parse(r)
	if err != nil {
		return false, err
	}
	s.pinVideo(v)
	defer s.releaseVideo(v)
	if receipt.path != filepath.Join(s.cfg.StorageDir, "videos", v.key+".mp4") || !receipt.Unchanged() {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := s.recordVideo(ctx, v); err != nil {
		return false, err
	}
	return true, s.recordSongVideo(ctx, id, v)
}

func (r *LocalReceipt) Unchanged() bool {
	if r == nil {
		return false
	}
	info, err := os.Stat(r.path)
	return err == nil && os.SameFile(r.info, info) && r.info.Size() == info.Size() && r.info.ModTime() == info.ModTime() && r.info.Mode() == info.Mode()
}

func CheckLocalReceipt(ctx context.Context, storageDir, target string) (bool, *LocalReceipt, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, nil, err
	}
	parser := &Server{cfg: DefaultConfig()}
	v, err := parser.parse(r)
	if err != nil {
		return false, nil, err
	}
	path := filepath.Join(storageDir, "videos", v.key+".mp4")
	info, _ := os.Stat(path)
	err = checkFile(ctx, path, v)
	if ctx.Err() != nil {
		return false, nil, ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidCache) {
		return false, nil, nil
	}
	var receipt *LocalReceipt
	if err == nil && info != nil {
		receipt = &LocalReceipt{path: path, info: info}
		if !receipt.Unchanged() {
			receipt = nil
		}
	}
	return err == nil, receipt, err
}
