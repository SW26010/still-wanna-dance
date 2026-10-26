package cacheproxy

import (
	"context"
	"errors"
	"fmt"

	"still-wanna-dance/internal/blocksched"
)

// ErrLocalStorage stops upstream fallback. Restart the engine after repairing storage.
var ErrLocalStorage = errors.New("local storage unavailable")

func isLocalStorageError(err error) bool {
	return err != nil && (errors.Is(err, ErrLocalStorage) ||
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, errUpstreamDownload) && !errors.Is(err, errBusy) && !errors.Is(err, ErrNotRetained) &&
			!errors.Is(err, blocksched.ErrClosed))
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
	if errors.Is(next, context.Canceled) || errors.Is(next, context.DeadlineExceeded) || errors.Is(next, blocksched.ErrClosed) {
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
		// A terminal storage failure affects every pending admission and
		// continuing resource, including those with no active download.
		s.notifyCapacityLocked()
		for key := range s.resourceChanges {
			s.notifyResourceChangedLocked(key)
		}
	}
	s.mu.Unlock()
	return err
}

// StorageError reports a storage failure retained for this engine lifetime.
func (s *Server) StorageError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.storageFailure, s.Store.Error())
}
