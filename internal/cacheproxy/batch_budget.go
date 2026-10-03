package cacheproxy

import (
	"context"
	"errors"
	"sync"
)

// ErrBatchBudget is a normal batch stop, not a failed download.
var ErrBatchBudget = errors.New("下载补齐容量预算不足")

type batchBudgetKey struct{}
type batchSongBudgetKey struct{}
type batchSongBudget struct {
	keys     map[string]bool // guarded by batchBudget.mu
	admitted bool
}
type budgetEntry struct {
	size  int64
	users int
	keep  bool
}
type batchBudget struct {
	mu      sync.Mutex
	entries map[string]budgetEntry
	stopped bool
}

// WithBatchBudget accounts for retained videos and concurrent reservations.
// Keep the initial inventory and completed reservations charged for this run:
// eviction must not turn a bounded fill into a rolling full-library download.
func (s *Server) WithBatchBudget(ctx context.Context) context.Context {
	if s.cfg.MaxCacheBytes == 0 {
		return ctx
	}
	b := &batchBudget{entries: make(map[string]budgetEntry)}
	s.retentionMu.Lock()
	for key, item := range s.retained {
		b.entries[key] = budgetEntry{size: item.size, keep: true}
	}
	s.retentionMu.Unlock()
	return context.WithValue(ctx, batchBudgetKey{}, b)
}

// WithBatchSongBudget keeps reservations alive across a song's route retries.
// Releasing the scope returns failed reservations only after all retries end.
func WithBatchSongBudget(ctx context.Context) (context.Context, func()) {
	b, _ := ctx.Value(batchBudgetKey{}).(*batchBudget)
	if b == nil {
		return ctx, func() {}
	}
	song := &batchSongBudget{keys: make(map[string]bool)}
	return context.WithValue(ctx, batchSongBudgetKey{}, song), func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for key := range song.keys {
			e := b.entries[key]
			e.users--
			if e.users == 0 && !e.keep {
				delete(b.entries, key)
			} else {
				b.entries[key] = e
			}
		}
	}
}

func (s *Server) reserveBatchVideo(ctx context.Context, v video) (func(bool), error) {
	b, _ := ctx.Value(batchBudgetKey{}).(*batchBudget)
	if b == nil {
		return func(bool) {}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	song, _ := ctx.Value(batchSongBudgetKey{}).(*batchSongBudget)
	if b.stopped && (song == nil || !song.admitted) {
		return nil, ErrBatchBudget
	}
	// A different fallback version replaces this song's failed reservation.
	// Published resources and other users retain their own capacity charge.
	if song != nil {
		for key := range song.keys {
			e := b.entries[key]
			if key == v.key || e.keep {
				continue
			}
			e.users--
			if e.users == 0 {
				delete(b.entries, key)
			} else {
				b.entries[key] = e
			}
			delete(song.keys, key)
		}
	}
	// Include videos published by playback since the batch began, without
	// double-counting reservations that have already been published.
	s.retentionMu.Lock()
	for key, item := range s.retained {
		e := b.entries[key]
		if item.size > e.size {
			e.size = item.size
		}
		e.keep = true
		b.entries[key] = e
	}
	s.retentionMu.Unlock()
	var used int64
	for _, e := range b.entries {
		used += e.size
	}
	e := b.entries[v.key]
	extra := v.size - e.size
	if extra < 0 {
		extra = 0
	}
	if extra > 0 && (used >= s.cfg.MaxCacheBytes || extra > s.cfg.MaxCacheBytes-used) {
		b.stopped = true
		return nil, ErrBatchBudget
	}
	e.size += extra
	if song != nil && !song.keys[v.key] {
		song.admitted = true
		song.keys[v.key] = true
		e.users++ // hold until the song's entire fallback sequence finishes
	}
	e.users++
	b.entries[v.key] = e
	return func(success bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		e := b.entries[v.key]
		e.users--
		e.keep = e.keep || success
		if e.users == 0 && !e.keep {
			delete(b.entries, v.key)
		} else {
			b.entries[v.key] = e
		}
	}, nil
}
