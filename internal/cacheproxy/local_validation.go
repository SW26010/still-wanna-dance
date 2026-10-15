package cacheproxy

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"time"
)

// Bound lock storage while coalescing checks of a resource across scanners and
// playback in this process.
type checkLocks [64]chan struct{}

func newCheckLocks() checkLocks {
	var locks checkLocks
	for i := range locks {
		locks[i] = make(chan struct{}, 1)
	}
	return locks
}

func (locks *checkLocks) acquire(ctx context.Context, key string) (func(), error) {
	slot := locks.slot(key)
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (locks *checkLocks) slot(key string) chan struct{} {
	return locks[crc32.ChecksumIEEE([]byte(key))%uint32(len(locks))]
}

var verificationLocks = newCheckLocks()

// Download finalization shares the local validation budget with cache hits.
// Release the slot before publication, and let shutdown cancel queued checks.
func (s *Server) checkDownloadedFile(ctx context.Context, file *os.File, v video) error {
	select {
	case s.localChecks <- struct{}{}:
		defer func() { <-s.localChecks }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := checkOpenFile(ctx, file, v); err != nil {
		return fmt.Errorf("%w: %w", errUpstreamDownload, err)
	}
	return nil
}

func validateLocalFile(ctx context.Context, root string, f *verifiedFile, v video, force bool) (bool, error) {
	unlock, err := verificationLocks.acquire(ctx, root+v.key)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	before, err := f.file.Stat()
	if err != nil {
		return false, err
	}
	// Published MD5 files are immutable by contract. Ordinary use never hashes
	// them again, including after restart or a metadata change.
	if !force {
		if !before.Mode().IsRegular() {
			return false, errInvalidCache
		}
		f.info, f.checkedAt = before, time.Now()
		return true, nil
	}
	if err := checkOpenFile(ctx, f.file, v); err != nil {
		return false, err
	}
	after, err := f.file.Stat()
	if err != nil {
		return false, err
	}
	if !sameVerifiedFile(before, after) {
		return false, fmt.Errorf("%w: cached file changed during validation", errInvalidCache)
	}
	f.info, f.checkedAt = after, time.Now()
	return false, nil
}
