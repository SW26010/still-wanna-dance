package cacheproxy

import (
	"time"

	"still-wanna-dance/internal/blocksched"
)

// planUseLocked is the complete source projection, including locally readable
// and completed blocks. Only the download adapter filters missing blocks.
// Called with Server.mu; handoff identity/expiry belongs here, never to a task.
func (s *Server) planUseLocked(key string, now time.Time) (map[int64]map[blockSource]plannedNeed, bool) {
	if s.expireUseLocked(now) {
		s.notifyCapacityLocked()
	}
	plan := make(map[int64]map[blockSource]plannedNeed)
	active := false
	add := func(span useSpan, source blockSource, clock *playbackClock, until time.Time, origin demandSource, revision int64) {
		if span.key != key {
			return
		}
		active = true
		for block := span.first; block < span.last; block++ {
			if plan[block] == nil {
				plan[block] = make(map[blockSource]plannedNeed)
			}
			ready := time.Time{}
			if clock != nil {
				ready = clock.ready(block * rangeBlockSize)
			}
			plan[block][source] = plannedNeed{need: blocksched.Need{Owner: key, ReadyBy: ready, LeaseUntil: until}, source: origin, revision: revision}
		}
	}
	for d := range s.demands {
		if !d.exit.active(d.ctx) {
			s.removeDemandLocked(d)
			continue
		}
		if d.queue {
			continue // Queue ownership outlives its preparation waiter.
		}
		kind, origin := "batch", sourceBatch
		if d.playback {
			kind, origin = "get", sourceGET
		}
		add(d.useSpan(), blockSource{kind, d}, d.clock, now.Add(sourceLease), origin, 0)
	}
	for _, q := range s.prefetchSources {
		clock := playbackClock{at: q.startAt, media: q.timing}
		add(q.use, blockSource{"queue", q}, &clock, now.Add(sourceLease), q.origin(), int64(q.position))
	}
	for _, h := range s.useHandoffs {
		if h.until.After(now) {
			add(h.span, blockSource{"handoff", h}, &h.clock, h.until, sourceHandoff, 0)
		}
	}
	return plan, active
}
