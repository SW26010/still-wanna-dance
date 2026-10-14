package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

const (
	priorityPlayback = iota
	priorityGrace
	priorityQueueHead
	priorityQueue
	priorityPrefetch
	priorityNone
)

const playbackGracePeriod = time.Second

// Smaller ranks are more urgent. Position orders P3 only; it is not a weight.
type downloadRank struct{ level, position int }

func (a downloadRank) before(b downloadRank) bool {
	return a.level < b.level || a.level == b.level && a.position < b.position
}

var noDemand = downloadRank{level: priorityNone}

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
		if r.level != priorityPlayback && rank.before(r) && (victim == nil || victimRank.before(r) || victimRank == r && f.id < victim.id) {
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
		if other != d && other.waiting && other.key != d.key && s.demandRankLocked(other) != noDemand && s.resourceRankLocked(other.key).before(rank) {
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
			return downloadRank{level: priorityQueueHead}
		}
		return downloadRank{level: priorityQueue, position: position}
	}
	return noDemand
}

func (s *Server) demandRankLocked(d *downloadDemand) downloadRank {
	if d.ctx.Err() != nil {
		return noDemand
	}
	if d.playback {
		return downloadRank{level: priorityPlayback}
	}
	if d.queue {
		return s.queueRankLocked(d.song)
	}
	return downloadRank{level: priorityPrefetch}
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
	if grace := s.playbackRankLocked(key, rank.level == priorityPlayback); grace.before(rank) {
		rank = grace
	}
	return rank
}

// A zero deadline records live GET demand, including requests waiting for a
// slot. A nonzero deadline protects only an existing download/checkpoint; it
// never creates a waiter or starts work. All fields are protected by Server.mu.
type playbackGrace struct {
	until time.Time
	timer *time.Timer
}

func (s *Server) playbackRankLocked(key string, active bool) downloadRank {
	state := s.playbackGrace[key]
	if s.closed {
		return noDemand
	}
	if active {
		if state == nil || !state.until.IsZero() {
			s.clearPlaybackGraceLocked(key)
			if s.playbackGrace == nil {
				s.playbackGrace = make(map[string]*playbackGrace)
			}
			s.playbackGrace[key] = &playbackGrace{}
		}
		return downloadRank{level: priorityPlayback}
	}
	if state == nil {
		return noDemand
	}
	if f := s.flights[key]; (f == nil || f.completed) && s.paused[key] == nil {
		s.clearPlaybackGraceLocked(key)
		return noDemand
	}
	if state.until.IsZero() {
		state.until = time.Now().Add(playbackGracePeriod)
		state.timer = time.AfterFunc(playbackGracePeriod, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// Stop cannot retract an already-running callback. A reconnect
			// replaces the state, so an old timer cannot demote a new GET.
			if s.playbackGrace[key] == state {
				s.expirePlaybackGraceLocked(key)
			}
		})
		// Also reached when cancellation is observed before its AfterFunc.
		// Downgrade directly to P1 without exposing a no-demand interval.
		s.notifyCapacityLocked()
		if s.cfg.Logger != nil {
			s.cfg.Logger.Info("playback_grace_started", "resource_key", key, "grace_ms", playbackGracePeriod.Milliseconds())
		}
	}
	if time.Now().Before(state.until) {
		return downloadRank{level: priorityGrace}
	}
	s.expirePlaybackGraceLocked(key)
	return noDemand
}

func (s *Server) expirePlaybackGraceLocked(key string) {
	s.clearPlaybackGraceLocked(key)
	s.notifyCapacityLocked()
	if s.cfg.Logger != nil {
		s.cfg.Logger.Info("playback_grace_expired", "resource_key", key)
	}
}

func (s *Server) clearPlaybackGraceLocked(key string) {
	if state := s.playbackGrace[key]; state != nil {
		if state.timer != nil {
			state.timer.Stop()
		}
		delete(s.playbackGrace, key)
	}
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
	s.resourceRankLocked(v.key)
	s.notifyCapacityLocked()
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			s.resourceRankLocked(v.key)
			delete(s.demands, d)
			s.resourceRankLocked(v.key)
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
