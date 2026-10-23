package cacheproxy

// Resource coordination observes source, availability and session changes for
// its own key. Subscriptions own the entry, including pending preparations that
// may be canceled before they ever register a source. All methods require mu.
type resourceChange struct {
	changed chan struct{}
	users   int
}

func (s *Server) watchResourceLocked(key string) *resourceChange {
	if s.resourceChanges == nil {
		s.resourceChanges = make(map[string]*resourceChange)
	}
	w := s.resourceChanges[key]
	if w == nil {
		w = &resourceChange{changed: make(chan struct{})}
		s.resourceChanges[key] = w
	}
	w.users++
	return w
}

func (s *Server) unwatchResourceLocked(key string) {
	w := s.resourceChanges[key]
	w.users--
	if w.users == 0 {
		delete(s.resourceChanges, key)
	}
}

func (s *Server) notifyResourceChangedLocked(key string) {
	if w := s.resourceChanges[key]; w != nil {
		close(w.changed)
		w.changed = make(chan struct{})
	}
}
