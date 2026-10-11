package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// Smaller ranks are more urgent. Position orders P2 only; it is not a weight.
type downloadRank struct{ level, position int }

func (a downloadRank) before(b downloadRank) bool {
	return a.level < b.level || a.level == b.level && a.position < b.position
}

var noDemand = downloadRank{level: 4}

var errPreempted = errors.Join(context.Canceled, errors.New("download preempted by higher priority demand"))

// With mu held, select only one victim until its worker has really exited.
// A background-limit wait can only reclaim a background-owned slot.
func (s *Server) preemptLocked(key string, rank downloadRank, backgroundFull bool) bool {
	var victim *flight
	victimRank := rank
	for resource, f := range s.flights {
		if f.preempted {
			return true
		}
		if resource == key || f.cancel == nil || backgroundFull && !f.background {
			continue
		}
		r := s.resourceRankLocked(resource)
		if r.level != 0 && rank.before(r) && (victim == nil || victimRank.before(r) || victimRank == r && f.id < victim.id) {
			victim, victimRank = f, r
		}
	}
	if victim == nil {
		return false
	}
	victim.preempted = true
	victim.log.Info("cache_task_preempted", "priority", victimRank.level, "waiting_priority", rank.level,
		"reason", "higher_priority_demand", "waiting_resource", key)
	victim.cancel(errPreempted)
	return true
}

func (s *Server) earlierWaiterLocked(d *downloadDemand, rank downloadRank) bool {
	for other := range s.demands {
		if other != d && other.waiting && other.key != d.key && s.demandRankLocked(other).before(rank) {
			return true
		}
	}
	return false
}

type queueDemandKey struct{}
type downloadDemand struct {
	ctx                      context.Context
	key, song                string
	queue, playback, waiting bool
}

// UpdatePrefetchQueue is independent of the full queue's file retention pins.
// The owner serializes snapshots and cancels obsolete callers before updating.
func (s *Server) UpdatePrefetchQueue(ids []int64, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefetchOrder = make(map[string]int, len(ids))
	for i, id := range ids {
		if id > 0 {
			song := strconv.FormatInt(id, 10)
			if _, exists := s.prefetchOrder[song]; !exists {
				s.prefetchOrder[song] = i
			}
		}
	}
	for song := range s.prefetchResources {
		if _, ok := s.prefetchOrder[song]; reset || !ok {
			delete(s.prefetchResources, song)
		}
	}
	s.notifyCapacityLocked()
}

func (s *Server) queueRankLocked(song string) downloadRank {
	if position, ok := s.prefetchOrder[song]; ok {
		if position == 0 {
			return downloadRank{level: 1}
		}
		return downloadRank{level: 2, position: position}
	}
	return noDemand
}

func (s *Server) demandRankLocked(d *downloadDemand) downloadRank {
	if d.ctx.Err() != nil {
		return noDemand
	}
	if d.playback {
		return downloadRank{}
	}
	if d.queue {
		return s.queueRankLocked(d.song)
	}
	return downloadRank{level: 3}
}

func (s *Server) resourceRankLocked(key string) downloadRank {
	rank := noDemand
	for d := range s.demands {
		if d.key == key {
			if r := s.demandRankLocked(d); r.before(rank) {
				rank = r
			}
		}
	}
	for song, resource := range s.prefetchResources {
		if resource == key {
			if r := s.queueRankLocked(song); r.before(rank) {
				rank = r
			}
		}
	}
	return rank
}

func (s *Server) addDemand(ctx context.Context, v video, playback bool) (*downloadDemand, func()) {
	queue, _ := ctx.Value(queueDemandKey{}).(bool)
	d := &downloadDemand{ctx: ctx, key: v.key, song: v.songID, queue: queue, playback: playback}
	s.mu.Lock()
	if s.demands == nil {
		s.demands = make(map[*downloadDemand]bool)
	}
	s.demands[d] = true
	if queue && ctx.Err() == nil && s.queueRankLocked(v.songID) != noDemand {
		if s.prefetchResources == nil {
			s.prefetchResources = make(map[string]string)
		}
		s.prefetchResources[v.songID] = v.key
	}
	s.notifyCapacityLocked()
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.demands, d)
			s.notifyCapacityLocked()
			s.mu.Unlock()
		})
	}
	stop := context.AfterFunc(ctx, release)
	return d, func() { stop(); release() }
}

func (s *Server) notifyCapacityLocked() {
	close(s.capacityChanged)
	s.capacityChanged = make(chan struct{})
}
