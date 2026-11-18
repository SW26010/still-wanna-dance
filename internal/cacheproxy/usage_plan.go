package cacheproxy

import (
	"math"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func allUseBlocks(key string) useSpan {
	return useSpan{key, 0, math.MaxInt64}
}

// visitUseLocked owns the complete source calculation, including locally
// readable/completed blocks. Consumers collect only the result they need.
// Called with Server.mu; handoff identity/expiry belongs here, never to a task.
func (s *Server) visitUseLocked(scope useSpan, now time.Time, visit func(useBlockID, blockSource, plannedNeed)) bool {
	active := false
	add := func(span useSpan, source blockSource, clock *playbackClock, until time.Time, closed bool, idleBase time.Time, order uint64) {
		if scope.key != "" && span.key != scope.key {
			return
		}
		active = true
		for block := max(span.first, scope.first); block < min(span.last, scope.last); block++ {
			id := useBlockID{span.key, block}
			ready := time.Time{}
			if clock != nil {
				ready = clock.ready(block * s.cfg.BlockSize)
			}
			p := plannedNeed{need: blocksched.Need{Owner: span.key, ReadyBy: ready, LeaseUntil: until, WindowClosed: closed, OpportunisticOrder: order}}
			if !idleBase.IsZero() {
				// No GET consumes this queue. A future estimate still supplies
				// absolute entry times, but once reached, only distance from the
				// head determines eligibility. Gate both snapshots now so they
				// cannot enter by wall time between source refreshes.
				distance := ready.Sub(idleBase)
				ahead := s.cfg.PrepareAhead
				if ahead <= 0 {
					ahead = blocksched.DefaultPrepareAhead
				}
				p.need.WindowClosed = distance > ahead
				p.protectionClosed = distance > s.protectionAhead()
			}
			visit(id, source, p)
		}
	}
	for d := range s.demands {
		if d.queue || (scope.key != "" && d.key != scope.key) {
			continue // Queue ownership outlives its preparation waiter.
		}
		kind := "batch"
		until := now.Add(sourceLease)
		if d.playback {
			kind = "get"
			// Until attachment, an expired missing prefix disqualifies this
			// GET's whole span from downloads, while local delivery stays live.
			if d.flight == nil && d.clock != nil && sourceExpired(d.clock.ready(d.firstBlock*s.cfg.BlockSize), now) {
				if at := s.missingDeadline(d.useSpan(), *d.clock, s.firstMissingLocal); !at.IsZero() && !at.After(now) {
					until = time.Time{}
				}
			}
		}
		add(d.useSpan(), blockSource{kind, d}, d.clock, until, false, time.Time{}, d.preparationOrder)
	}
	var idleBase time.Time
	if s.queuePlaybackEndLocked().IsZero() {
		idleBase = s.queueStart
	}
	for _, q := range s.prefetchSources {
		clock := playbackClock{at: q.startAt, media: q.timing}
		add(q.use, blockSource{"queue", q}, &clock, now.Add(sourceLease), false, idleBase, 0)
	}
	for _, h := range s.useHandoffs {
		add(h.span, blockSource{"handoff", h}, &h.clock, h.until, true, time.Time{}, 0)
	}
	return active
}

// Read snapshots first bring delayed exits up to date. Mutations recompute
// protection from the same projection without recursively delivering exits.
func (s *Server) refreshUseSourcesLocked(now time.Time) {
	if keys := s.expireUseLocked(now); len(keys) > 0 {
		s.notifyUseChangedLocked(keys...)
	}
	for d := range s.demands {
		if !d.exit.active(d.ctx) {
			s.removeDemandLocked(d)
		}
	}
	// A delayed timer must not leave snapshots using an obsolete idle clock.
	if next := s.nextQueueRetimeLocked(); !next.IsZero() && !next.After(now) {
		s.notifyUseChangedLocked(s.retimeQueueLocked(now)...)
	}
}

func (s *Server) collectUsePlanLocked(key string, now time.Time) (map[int64]blockUse, bool) {
	plan := make(map[int64]blockUse)
	active := s.visitUseLocked(allUseBlocks(key), now, func(id useBlockID, _ blockSource, p plannedNeed) {
		plan[id.block] = mergeUse(plan[id.block], p.use(now))
	})
	return plan, active
}
