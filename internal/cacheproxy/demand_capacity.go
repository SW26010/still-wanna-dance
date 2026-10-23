package cacheproxy

import (
	"container/heap"
	"errors"
	"time"
)

const maxUseBlocks = 4096
const backgroundUseBlocks = maxUseBlocks * 70 / 100

// ErrDemandCapacity is a temporary refusal of new unique block usage. Existing
// sources are never evicted, and downloads/local hits do not release this quota.
var ErrDemandCapacity = errors.New("block usage demand capacity reached")

type useBlockID struct {
	key   string
	block int64
}

type useSpan struct {
	key         string
	first, last int64
}

type useHandoff struct {
	span  useSpan
	until time.Time
	clock playbackClock
}

type useHandoffHeap []*useHandoff

func (h useHandoffHeap) Len() int           { return len(h) }
func (h useHandoffHeap) Less(i, j int) bool { return h[i].until.Before(h[j].until) }
func (h useHandoffHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *useHandoffHeap) Push(v any)        { *h = append(*h, v.(*useHandoff)) }
func (h *useHandoffHeap) Pop() any {
	a := *h
	v := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	return v
}

// Called with Server.mu. This index counts source contributions to unique
// blocks; it deliberately knows nothing about files or download completion.
func (s *Server) reserveUseLocked(span useSpan, playback bool) error {
	return s.replaceUseLocked(useSpan{}, span, playback)
}

func (r useSpan) contains(key string, block int64) bool {
	return r.key == key && block >= r.first && block < r.last
}

func (s *Server) replaceUseLocked(old, span useSpan, playback bool) error {
	// Timer delivery may lag behind a registration at the expiry boundary.
	// Admission uses the actual deadline, never the callback's scheduling time.
	if keys := s.expireUseLocked(time.Now()); len(keys) > 0 {
		s.notifyUseChangedLocked(keys...)
	}
	added, removed := 0, 0
	for b := span.first; b < span.last; b++ {
		if !old.contains(span.key, b) && s.useBlocks[useBlockID{span.key, b}] == 0 {
			added++
		}
	}
	for b := old.first; b < old.last; b++ {
		if !span.contains(old.key, b) && s.useBlocks[useBlockID{old.key, b}] == 1 {
			removed++
		}
	}
	limit := backgroundUseBlocks
	if playback {
		limit = maxUseBlocks
	}
	if added > removed && len(s.useBlocks)-removed+added > limit {
		return ErrDemandCapacity
	}
	if s.useBlocks == nil {
		s.useBlocks = make(map[useBlockID]int)
	}
	for b := span.first; b < span.last; b++ {
		if !old.contains(span.key, b) {
			s.useBlocks[useBlockID{span.key, b}]++
		}
	}
	for b := old.first; b < old.last; b++ {
		if !span.contains(old.key, b) {
			s.releaseUseLocked(useSpan{old.key, b, b + 1}, time.Now())
		}
	}
	return nil
}

func (s *Server) releaseUseLocked(span useSpan, at time.Time) {
	released := false
	for b := span.first; b < span.last; b++ {
		id := useBlockID{span.key, b}
		if n := s.useBlocks[id]; n > 1 {
			s.useBlocks[id] = n - 1
		} else if n == 1 {
			s.endLocalUseLocked(id, at)
			delete(s.useBlocks, id)
			released = true
		}
	}
	if released {
		s.notifyCapacityLocked()
	}
}

func (s *Server) endUseLocked(span useSpan, at time.Time, grace time.Duration, clock playbackClock, origin demandSource) {
	if span.first >= span.last {
		return
	}
	if grace > 0 && !s.closed {
		// Transfer the existing reference. Handoff never expands the source's
		// remaining range, even when some blocks are already locally readable.
		heap.Push(&s.useHandoffs, &useHandoff{span, at.Add(grace), clock})
		if s.cfg.Logger != nil {
			s.cfg.Logger.Debug("block_demand_handoff", "resource_key", span.key, "first_block", span.first, "last_block", span.last,
				"from", origin.String(), "reason", "source_gone", "withdrawn_at", at,
				"first_ready_by", clock.ready(span.first*rangeBlockSize), "lease_until", at.Add(grace))
		}
	} else {
		s.releaseUseLocked(span, at)
	}
}

func (s *Server) armUseTimerLocked() {
	if s.useTimer != nil {
		s.useTimer.Stop()
		s.useTimer = nil
	}
	if s.closed {
		return
	}
	next := s.nextQueueRetimeLocked()
	if len(s.useHandoffs) > 0 {
		next = earlier(next, s.useHandoffs[0].until)
	}
	if next.IsZero() {
		return
	}
	s.useTimer = time.AfterFunc(max(0, time.Until(next)), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return
		}
		s.refreshUseSourcesLocked(time.Now())
	})
}

func (s *Server) expireUseLocked(now time.Time) []string {
	var changed []string
	for len(s.useHandoffs) > 0 && !s.useHandoffs[0].until.After(now) {
		h := heap.Pop(&s.useHandoffs).(*useHandoff)
		s.releaseUseLocked(h.span, h.until)
		changed = append(changed, h.span.key)
	}
	return changed
}

// DemandCapacityChanged is captured before a registration attempt, so callers
// can wait for normal source release without polling or losing a wakeup.
func (s *Server) DemandCapacityChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.demandCapacityChangedLocked()
}

func (s *Server) demandCapacityChangedLocked() <-chan struct{} {
	if s.capacityChanged == nil {
		s.capacityChanged = make(chan struct{})
	}
	return s.capacityChanged
}

// Unique usage release and terminal engine events wake global admission waiters.
// Lazily recreate the channel so a multi-block release coalesces its signal.
func (s *Server) notifyCapacityLocked() {
	if s.capacityChanged != nil {
		close(s.capacityChanged)
		s.capacityChanged = nil
	}
}

func (d *downloadDemand) useSpan() useSpan {
	return useSpan{d.key, d.firstBlock, d.lastBlock}
}
