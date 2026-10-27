package cacheproxy

// RequestHistoryChanges observes committed GET history and retention changes.
// Capturing the revision and wakeup together prevents lost updates. It does not
// start observation or an engine, retain a reader, or do database I/O.
func (s *Store) RequestHistoryChanges() (revision uint64, changed <-chan struct{}, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyRevision, s.historyChanged, s.closed
}

func (s *Store) notifyHistoryChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.historyRevision++
	close(s.historyChanged)
	s.historyChanged = make(chan struct{})
}
