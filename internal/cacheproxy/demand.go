package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// Resource ownership feeds the block-demand adapter. It carries no scheduling rank.
var errPreempted = errors.Join(context.Canceled, errors.New("download paused without active demand"))

type queueDemandKey struct{}
type downloadDemand struct {
	ctx                      context.Context
	key, song                string
	queue, playback, waiting bool
}

func (s *Server) queueContainsLocked(song string) bool { _, ok := s.prefetchOrder[song]; return ok }
func (s *Server) UpdatePrefetchQueue(ids []int64, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefetchOrder = make(map[string]int, len(ids))
	for i, id := range ids {
		if id > 0 {
			song := strconv.FormatInt(id, 10)
			if _, ok := s.prefetchOrder[song]; !ok {
				s.prefetchOrder[song] = i
			}
		}
	}
	for song := range s.prefetchResources {
		if reset || !s.queueContainsLocked(song) {
			delete(s.prefetchResources, song)
		}
	}
	s.notifyCapacityLocked()
}
func (s *Server) addDemand(ctx context.Context, v video, playback bool) (*downloadDemand, func()) {
	queue, _ := ctx.Value(queueDemandKey{}).(bool)
	d := &downloadDemand{ctx: ctx, key: v.key, song: v.songID, queue: queue, playback: playback}
	s.mu.Lock()
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	s.demands[d] = true
	if queue && ctx.Err() == nil && s.queueContainsLocked(v.songID) {
		if s.prefetchResources == nil {
			s.prefetchResources = make(map[string]string)
		}
		s.prefetchResources[v.songID] = v.key
	}
	s.notifyCapacityLocked()
	s.mu.Unlock()
	release := sync.OnceFunc(func() { s.mu.Lock(); delete(s.demands, d); s.notifyCapacityLocked(); s.mu.Unlock() })
	stop := context.AfterFunc(ctx, release)
	return d, func() { stop(); release() }
}
func (s *Server) notifyCapacityLocked() {
	close(s.capacityChanged)
	s.capacityChanged = make(chan struct{})
}
