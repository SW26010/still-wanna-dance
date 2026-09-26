package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// CheckLocal validates existing bytes only. It never starts an engine, downloads,
// publishes, deletes, or updates usage. It may persist verification metadata.
// A missing/corrupt video is a scan result; an unreadable file is an error and
// must not replace a successful snapshot.
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
	return err == nil && os.SameFile(r.info, info) && r.info.Size() == info.Size() && r.info.ModTime().Equal(info.ModTime()) && r.info.Mode() == info.Mode()
}

// LocalChecker shares durable verification with playback without starting an engine.
// Close must be called after all checks have finished.
type LocalChecker struct {
	store   *verificationStore
	force   bool
	mu      sync.Mutex
	results map[string]*LocalReceipt
}

type LocalCheckResult struct {
	Hit     bool
	Reused  bool
	Corrupt bool
	Receipt *LocalReceipt
}

func NewLocalChecker(storageDir string, force bool) (*LocalChecker, error) {
	store, err := openVerificationStore(storageDir)
	if err != nil {
		return nil, err
	}
	return &LocalChecker{store: store, force: force, results: make(map[string]*LocalReceipt)}, nil
}

func (c *LocalChecker) Close() error { return c.store.db.Close() }

func (c *LocalChecker) Check(ctx context.Context, target string) (LocalCheckResult, error) {
	var result LocalCheckResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return result, err
	}
	v, err := parseVideo(r, DefaultConfig().MaxFileBytes)
	if err != nil {
		return result, err
	}
	// Forced scans hash shared resources once, checking attributes for each song.
	unlock, err := localScanLocks.acquire(ctx, c.store.root+v.key)
	if err != nil {
		return result, err
	}
	defer unlock()
	c.mu.Lock()
	prior := c.results[v.key]
	c.mu.Unlock()
	if prior != nil && prior.Unchanged() {
		return LocalCheckResult{Hit: true, Reused: true, Receipt: prior}, ctx.Err()
	}
	path := filepath.Join(c.store.root, "videos", v.key+".mp4")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	checked := &verifiedFile{file: file}
	result.Reused, err = c.store.validate(ctx, checked, v, c.force)
	if errors.Is(err, errInvalidCache) {
		result.Corrupt = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	receipt := &LocalReceipt{path: path, info: checked.info}
	if !receipt.Unchanged() {
		return result, errInvalidCache
	}
	result.Hit, result.Receipt = true, receipt
	c.mu.Lock()
	c.results[v.key] = receipt
	c.mu.Unlock()
	return result, nil
}

var localScanLocks = newCheckLocks()

func CheckLocalReceipt(ctx context.Context, storageDir, target string) (bool, *LocalReceipt, error) {
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	checker, err := NewLocalChecker(storageDir, false)
	if err != nil {
		return false, nil, err
	}
	defer checker.Close()
	result, err := checker.Check(ctx, target)
	return result.Hit, result.Receipt, err
}
