package cacheproxy

import (
	"context"
	"strconv"
	"time"
)

const queueHandoffGrace = 30 * time.Second

// SetQueueSongs replaces the full pending queue using in-memory reservations.
// It waits outside retentionMu only when a requested resource is already being
// removed; unrelated database/filesystem work does not block queue updates.
// Departures (including an empty snapshot) keep a bounded handoff reservation
// across playback probes and range requests. Active HTTP pins are independent.
func (s *Server) SetQueueSongs(ids []int64) {
	_ = s.UpdateQueueSongs(context.Background(), ids, false)
}

// ResetQueueSongs discards reservations from the previous listener/room,
// including handoffs. Active HTTP references remain independent.
func (s *Server) ResetQueueSongs(ids []int64) {
	_ = s.UpdateQueueSongs(context.Background(), ids, true)
}

// UpdateQueueSongs lets the owning queue coordinator cancel deletion waits.
// The owner serializes updates; no partial queue change is applied on cancellation.
func (s *Server) UpdateQueueSongs(ctx context.Context, ids []int64, reset bool) error {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	// A deletion already reserved precedes this queue update. Wait outside
	// the lock so publication and file removal can finish before reserving.
	for {
		var done chan struct{}
		for _, id := range ids {
			for key := range s.songResources[strconv.FormatInt(id, 10)] {
				if pending := s.deletingVideos[key]; pending != nil {
					done = pending
					break
				}
			}
			if done != nil {
				break
			}
		}
		if done == nil {
			break
		}
		s.retentionMu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			s.retentionMu.Lock()
			return ctx.Err()
		}
		s.retentionMu.Lock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := s.queueSongs
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
	if reset {
		clear(s.queueHandoffs)
	} else {
		if s.queueHandoffs == nil {
			s.queueHandoffs = make(map[string]time.Time)
		}
		deadline := time.Now().Add(queueHandoffGrace)
		for song := range previous {
			if !s.queueSongs[song] {
				s.queueHandoffs[song] = deadline
			}
		}
	}
	for song := range s.queueSongs {
		delete(s.queueHandoffs, song)
	}
	s.refreshHandoffsLocked(time.Now())
	s.requestRetentionLocked()
	return nil
}

// Rebuild resource deadlines from song ownership, preserving shared resources.
// Called under retentionMu; returns the next expiry for the retention worker.
func (s *Server) refreshHandoffsLocked(now time.Time) time.Time {
	s.handoffProtected = make(map[string]time.Time)
	var next time.Time
	for song, deadline := range s.queueHandoffs {
		if !now.Before(deadline) {
			delete(s.queueHandoffs, song)
			s.requestRetentionLocked()
			continue
		}
		if next.IsZero() || deadline.Before(next) {
			next = deadline
		}
		for key := range s.songResources[song] {
			if deadline.After(s.handoffProtected[key]) {
				s.handoffProtected[key] = deadline
			}
		}
	}
	return next
}

func (s *Server) queueReservedLocked(key string) bool {
	return s.queueProtected[key] || time.Now().Before(s.handoffProtected[key])
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
	if deadline := s.queueHandoffs[id]; deadline.After(s.handoffProtected[key]) {
		s.handoffProtected[key] = deadline
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
