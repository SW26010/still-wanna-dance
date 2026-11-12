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
	flight.log.Debug("download_verified", "bytes", n, "size_ok", true, "checksum_ok", true)
	return s.commitPublication(reservation, file, path, v, flight)
}

type publicationFile interface {
	Name() string
	Sync() error
	Close() error
}

func (s *Server) commitPublication(r *publicationReservation, file publicationFile, path string, v video, flight *flight) error {
	if err := r.ctx.Err(); err != nil {
		return context.Cause(r.ctx)
	}
	flight.progress.setStage("publish")
	// The staging file is private. Complete disk preparation before taking the
	// shared decision lock; admission and policies are rechecked afterward.
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
	item := retainedVideo{path: path, key: v.key, size: info.Size(), recent: info.ModTime().UnixMilli(), modified: info.ModTime().UnixNano()}
	return s.commitPreparedPublication(r, file.Name(), item)
}
