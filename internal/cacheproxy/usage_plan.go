package cacheproxy

import (
	"time"

	"still-wanna-dance/internal/blocksched"
)

// projectUseLocked is the complete source projection, including locally readable
// and completed blocks. Only the download adapter filters missing blocks.
// Called with Server.mu; handoff identity/expiry belongs here, never to a task.
func (s *Server) projectUseLocked(key string, now time.Time) (map[useBlockID]map[blockSource]plannedNeed, bool) {
	plan := make(map[useBlockID]map[blockSource]plannedNeed)
	active := false
	add := func(span useSpan, source blockSource, clock *playbackClock, until time.Time, origin demandSource) {
		if key != "" && span.key != key {
			return
		}
		active = true
		for block := span.first; block < span.last; block++ {
			id := useBlockID{span.key, block}
			if plan[id] == nil {
				plan[id] = make(map[blockSource]plannedNeed)
			}
			ready := time.Time{}
			if clock != nil {
				ready = clock.ready(block * rangeBlockSize)
			}
			plan[id][source] = plannedNeed{need: blocksched.Need{Owner: span.key, ReadyBy: ready, LeaseUntil: until}, source: origin}
		}
	}
	for d := range s.demands {
		if d.queue {
			continue // Queue ownership outlives its preparation waiter.
		}
		kind, origin := "batch", sourceBatch
		if d.playback {
			kind, origin = "get", sourceGET
		}
		add(d.useSpan(), blockSource{kind, d}, d.clock, now.Add(sourceLease), origin)
	}
	for _, q := range s.prefetchSources {
		clock := playbackClock{at: q.startAt, media: q.timing}
		add(q.use, blockSource{"queue", q}, &clock, now.Add(sourceLease), q.origin())
	}
	for _, h := range s.useHandoffs {
		add(h.span, blockSource{"handoff", h}, &h.clock, h.until, sourceHandoff)
	}
	return plan, active
}

// Read snapshots first bring delayed exits up to date. Mutations recompute
// protection from the same projection without recursively delivering exits.
func (s *Server) refreshUseSourcesLocked(now time.Time) {
	if s.expireUseLocked(now) {
		s.notifyCapacityLocked()
	}
	for d := range s.demands {
		if !d.exit.active(d.ctx) {
			s.removeDemandLocked(d)
		}
	}
}

func (s *Server) aggregateUseProjectionLocked(key string, now time.Time) (map[useBlockID]blockUse, bool) {
	projected, active := s.projectUseLocked(key, now)
	plan := make(map[useBlockID]blockUse, len(projected))
	for id, needs := range projected {
		plan[id] = aggregateUse(needs, now)
	}
	return plan, active
}

func (s *Server) usePlanLocked(key string, now time.Time) (map[int64]blockUse, bool) {
	s.refreshUseSourcesLocked(now)
	projected, active := s.aggregateUseProjectionLocked(key, now)
	plan := make(map[int64]blockUse, len(projected))
	for id, use := range projected {
		plan[id.block] = use
	}
	return plan, active
}
