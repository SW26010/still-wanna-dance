package cacheproxy

import "strconv"

// SetQueueSongs replaces the full pending queue. It only changes in-memory
// reservations, so queue updates never wait on database or filesystem I/O.
// Active HTTP requests retain their independent pins when a song leaves.
func (s *Server) SetQueueSongs(ids []int64) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	s.queueSongs = make(map[string]bool, len(ids))
	s.queueProtected = make(map[string]bool)
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		song := strconv.FormatInt(id, 10)
		s.queueSongs[song] = true
		for key := range s.songResources[song] {
			s.queueProtected[key] = true
		}
	}
	s.requestRetentionLocked()
}

func (s *Server) rememberSongResourceLocked(id, key string) {
	if s.songResources == nil {
		s.songResources = make(map[string]map[string]bool)
	}
	if s.songResources[id] == nil {
		s.songResources[id] = make(map[string]bool)
	}
	s.songResources[id][key] = true
	if s.queueSongs[id] {
		s.queueProtected[key] = true
	}
}

// Restore confirmed ownership before the engine starts accepting work. During
// prefetch, memory-only associations are also registered while pinned, before
// starting or joining a flight, so cancellation cannot drop queue protection.
func (s *Server) loadSongResources() error {
	rows, err := s.usage.db.Query(`SELECT song_id, version_key FROM song_videos`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return err
		}
		s.rememberSongResourceLocked(id, key)
	}
	return rows.Err()
}
