package cacheproxy

// Called synchronously while Store excludes retention and publication. Keep
// the engine's lock order here, never in database transaction code.
func (s *Server) databaseChanged(queue bool) {
	s.retentionMu.Lock()
	s.requestRetentionLocked()
	s.retentionMu.Unlock()
	if queue {
		s.mu.Lock()
		s.notifyQueuePreparationLocked()
		s.mu.Unlock()
	}
}
