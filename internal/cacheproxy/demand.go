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
	ctx             context.Context
	key, song       string
	queue, playback bool
	queueSource     queueSource
}

func (s *Server) queueContainsLocked(song string) bool { _, ok := s.prefetchOrder[song]; return ok }
func (s *Server) UpdatePrefetchQueue(ids []int64, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.prefetchGeneration
	s.prefetchGeneration = make(map[string]uint64, len(ids))
	s.prefetchOrder = make(map[string]int, len(ids))
	for i, id := range ids {
		if id > 0 {
			song := strconv.FormatInt(id, 10)
			if _, ok := s.prefetchOrder[song]; !ok {
				s.prefetchOrder[song] = i
				generation := previous[song]
				if reset || generation == 0 {
					generation = s.sequence.Add(1)
				}
				s.prefetchGeneration[song] = generation
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
	token, queue := ctx.Value(queueDemandKey{}).(queueSource)
	d := &downloadDemand{ctx: ctx, key: v.key, song: v.songID, queue: queue, playback: playback, queueSource: token}
	s.mu.Lock()
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	s.demands[d] = true
	if queue && ctx.Err() == nil && s.validQueueSourceLocked(d) {
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

func (s *Server) queueDemandContext(ctx context.Context, song string) context.Context {
	s.mu.Lock()
	token := queueSource{song, s.prefetchGeneration[song]}
	s.mu.Unlock()
	return context.WithValue(ctx, queueDemandKey{}, token)
}
func (s *Server) validQueueSourceLocked(d *downloadDemand) bool {
	return d.queueSource.song == d.song && d.queueSource.generation != 0 && s.prefetchGeneration[d.song] == d.queueSource.generation
}
