package cacheproxy

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Publication reads a complete pinned snapshot. The snapshot survives source
// changes/reclamation, while streaming readers keep their own block resource.
func (s *Server) publishBlocks(ctx context.Context, path string, v video, flight *flight) (result error) {
	reservation, err := s.reservePublication(ctx, v)
	if err != nil {
		return err
	}
	defer s.releasePublication(reservation)
	ctx = reservation.ctx
	defer func() {
		if errors.Is(result, context.Canceled) && errors.Is(context.Cause(ctx), ErrNotRetained) {
			result = ErrNotRetained
		}
	}()
	select {
	case s.localChecks <- struct{}{}:
		defer func() { <-s.localChecks }()
	case <-ctx.Done():
		return ctx.Err()
	}
	snapshot, err := flight.ranges.storage.snapshot()
	if errors.Is(err, errBlockUnavailable) {
		// Aggressive reclamation may remove completed blocks before the merge
		// snapshot is pinned, even with a positive or unlimited video budget.
		// This is a retention outcome, not a disk fault or a reason to refill.
		return ErrNotRetained
	}
	if err != nil {
		return err
	}
	defer snapshot.Close()
	file, err := os.CreateTemp(s.cfg.publishDir(), "download-publish-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	flight.progress.setStage("hash")
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(file, h), contextReader{ctx, io.NewSectionReader(snapshot, 0, v.size)})
	if err != nil {
		return err
	}
	if n != v.size || hex.EncodeToString(h.Sum(nil)) != v.key {
		flight.log.Warn("download_integrity_failed", "bytes", n, "expected_bytes", v.size, "size_ok", n == v.size, "checksum_ok", hex.EncodeToString(h.Sum(nil)) == v.key)
		return fmt.Errorf("%w: %w: merged video integrity mismatch", errUpstreamDownload, errInvalidCache)
	}
	flight.log.Info("download_verified", "bytes", n, "size_ok", true, "checksum_ok", true)
	return s.commitPublication(reservation, file, path, v, flight)
}

func (s *Server) commitPublishedFile(ctx context.Context, file *os.File, path string, v video, flight *flight) error {
	flight.progress.setStage("publish")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	info, err := os.Stat(file.Name())
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	s.retentionMu.Lock()
	item := retainedVideo{path: path, key: v.key, size: info.Size(), recent: info.ModTime().UnixMilli(), modified: info.ModTime().UnixNano()}
	s.retainVideoLocked(v.key, &item)
	if !s.cfg.CacheUnlimited && s.retainedBytes > s.cfg.MaxCacheBytes {
		s.requestRetentionLocked()
	}
	s.retentionMu.Unlock()
	return nil
}
