package cacheproxy

import (
	"context"
	"errors"
	"fmt"

	"still-wanna-dance/internal/blocksched"
)

// ErrLocalStorage stops upstream fallback. Restart the engine after repairing storage.
var ErrLocalStorage = errors.New("local cache storage unavailable")

func isLocalStorageError(err error) bool {
	return err != nil && (errors.Is(err, ErrLocalStorage) ||
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, errUpstreamDownload) && !errors.Is(err, errBusy) &&
			!errors.Is(err, blocksched.ErrClosed) && !errors.Is(err, blocksched.ErrPaused))
}

// Preserve a storage failure regardless of task completion/map iteration order.
// Do not join in an upstream marker: callers use that marker to permit fallback.
func preferDownloadError(current, next error) error {
	if next == nil || isLocalStorageError(current) {
		return current
	}
	if isLocalStorageError(next) {
		return next
	}
	if errors.Is(next, context.Canceled) || errors.Is(next, context.DeadlineExceeded) || errors.Is(next, blocksched.ErrClosed) || errors.Is(next, blocksched.ErrPaused) {
		return current
	}
	return next
}

func (s *Server) storageError(err error) error {
	if !isLocalStorageError(err) {
		return err
	}
	if !errors.Is(err, ErrLocalStorage) {
		err = fmt.Errorf("%w: %w", ErrLocalStorage, err)
	}
	s.mu.Lock()
	if s.storageFailure == nil {
		s.storageFailure = err
	}
	s.mu.Unlock()
	return err
}

// StorageError reports a storage failure retained for this engine lifetime.
func (s *Server) StorageError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storageFailure
}
