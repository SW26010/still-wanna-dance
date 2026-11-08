package cacheproxy

// The engine is attached for the entire accepted request lifetime, so Store
// remains open until these observations have been queued and flushed on close.
func (s *Store) recordRequest(event usageEvent)     { s.usage.record(event) }
func (s *Store) recordGET(songID, key string) int64 { return s.usage.startGET(songID, key) }
