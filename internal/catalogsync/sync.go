// Package catalogsync owns metadata repair progress, independent of network
// requests, scheduling and route monitoring.
package catalogsync

import (
	"context"
	"sync"
	"time"
)

type Options struct {
	SongCandidates func(context.Context) []int64
}

type Sync struct {
	mu        sync.Mutex
	options   Options
	updated   time.Time
	closed    bool
	attempted map[int64]bool
	songID    int64
	songAt    time.Time
}

func New(options Options) *Sync {
	return &Sync{options: options}
}

func (s *Sync) UpdatedAt() time.Time { s.mu.Lock(); defer s.mu.Unlock(); return s.updated }

// Updated records a business write, including valid responses reused by diagnostics.
func (s *Sync) Updated() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.updated = time.Now()
	}
}
func (s *Sync) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// SelectSong traverses current missing metadata in business priority order.
// The traversal belongs to this business scope, including manual diagnostics.
func (s *Sync) SelectSong(ctx context.Context, fallback int64) int64 {
	return s.SelectSongExcept(ctx, fallback, nil)
}

// SelectSongExcept shares the traversal while excluding songs already held or
// attempted by this caller. Exclusion does not count as a metadata attempt.
func (s *Sync) SelectSongExcept(ctx context.Context, fallback int64, excluded map[int64]bool) int64 {
	if ctx.Err() != nil {
		return 0
	}
	var ids []int64
	if s.options.SongCandidates != nil {
		ids = s.options.SongCandidates(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return 0
	}
	if fallback > 0 {
		s.songID, s.songAt = fallback, time.Now()
	} else if time.Since(s.songAt) < 10*time.Minute {
		fallback = s.songID
	}
	attempted := make(map[int64]bool, len(ids))
	var first, selected int64
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if s.attempted[id] {
			attempted[id] = true
		}
		if excluded[id] {
			continue
		}
		if first == 0 {
			first = id
		}
		if !s.attempted[id] && selected == 0 {
			selected = id
		}
	}
	if selected == 0 && first != 0 {
		selected = first
		clear(attempted)
	}
	s.attempted = attempted
	if selected == 0 {
		if excluded[fallback] {
			return 0
		}
		return fallback
	}
	s.attempted[selected] = true
	return selected
}
