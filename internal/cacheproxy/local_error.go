package cacheproxy

import (
	"context"
	"errors"
	"fmt"
)

// ErrLocalStorage stops upstream fallback. Restart the engine after repairing storage.
var ErrLocalStorage = errors.New("local cache storage unavailable")

func (s *Server) storageError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errUpstreamDownload) {
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
