package cacheproxy

import (
	"time"

	"still-wanna-dance/internal/blocksched"
)

const (
	DefaultLocalProtectionGrace = 300 * time.Second
	MaxLocalProtectionGrace     = 10000 * time.Second
)

// Local protection is independent of download execution and file availability.
// start is derived from current finite usage; until is an already established
// tail. A far-future update can neither start protection nor renew that tail.
type blockProtection struct {
	start, until time.Time
}

func (p blockProtection) active(now time.Time) bool {
	return !p.start.IsZero() && !now.Before(p.start) || p.until.After(now)
}

func (s *Server) protectionAhead() time.Duration {
	if s.cfg.LocalProtectAhead > 0 {
		return s.cfg.LocalProtectAhead
	}
	if s.cfg.PrepareAhead > 0 {
		return s.cfg.PrepareAhead
	}
	return blocksched.DefaultPrepareAhead
}

// Called with mu after a source mutation. Only the earliest finite usage time
// crosses into local protection; local state does not retain source identities.
// Cancellation/expiry is transferred or removed before its notification, so
// pending callbacks must not independently remove protection here.
func (s *Server) refreshUseProtectionLocked(scope useSpan) {
	ready := make(map[int64]time.Time)
	s.visitUseLocked(scope, time.Now(), func(id useBlockID, _ blockSource, p plannedNeed) {
		if s.useBlocks[id] > 0 {
			if at := p.protectionReady(); !at.IsZero() {
				ready[id.block] = earlier(ready[id.block], at)
			}
		}
	})

	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if s.protection == nil {
		s.protection = make(map[string]map[int64]blockProtection)
	}
	key := scope.key
	if blocks := s.protection[key]; blocks != nil {
		clearStart := func(b int64) {
			p, ok := blocks[b]
			if !ok {
				return
			}
			p.start = time.Time{}
			blocks[b] = p
			if _, ok := ready[b]; !ok && p.until.IsZero() {
				delete(blocks, b)
			}
		}
		// Consumption usually changes one block; a source/clock mutation may
		// change the whole resource. Walk the smaller set to clear old starts.
		if scope.last-scope.first < int64(len(blocks)) {
			for b := scope.first; b < scope.last; b++ {
				clearStart(b)
			}
		} else {
			for b := range blocks {
				if scope.contains(key, b) {
					clearStart(b)
				}
			}
		}
		if len(blocks) == 0 {
			delete(s.protection, key)
		}
	}
	ahead := s.protectionAhead()
	for b, at := range ready {
		if s.protection[key] == nil {
			s.protection[key] = make(map[int64]blockProtection)
		}
		p := s.protection[key][b]
		p.start = at.Add(-ahead)
		s.protection[key][b] = p
	}
	s.requestRetentionLocked()
}

// The final usage reference supplies its actual end time, including a fixed
// handoff deadline when a timer callback runs late. Other releases do not call
// this method. Losing just the in-window contributor creates no special grace.
func (s *Server) endLocalUseLocked(id useBlockID, at time.Time) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	blocks := s.protection[id.key]
	p, ok := blocks[id.block]
	if !ok {
		return
	}
	if !p.start.IsZero() && !at.Before(p.start) {
		p.until = maxTime(p.until, at.Add(s.cfg.LocalProtectionGrace))
	}
	p.start = time.Time{}
	blocks[id.block] = p
}

func (s *Server) blockProtectedLocked(key string, block int64, now time.Time) bool {
	return s.protection[key][block].active(now)
}

// Complete videos are still reclaimed as files: any protected block keeps the
// file, while temporary block storage can consult the exact block identity.
func (s *Server) videoProtectedLocked(key string) bool {
	now := time.Now()
	for _, p := range s.protection[key] {
		if p.active(now) {
			return true
		}
	}
	return false
}

func (s *Server) nextProtectionExpiryLocked(now time.Time) time.Time {
	var next time.Time
	for key, blocks := range s.protection {
		for b, p := range blocks {
			if !p.until.IsZero() && !p.until.After(now) {
				p.until = time.Time{}
				blocks[b] = p
			}
			if p.start.IsZero() && p.until.IsZero() {
				delete(blocks, b)
				continue
			}
			if p.until.After(now) && (next.IsZero() || p.until.Before(next)) {
				next = p.until
			}
		}
		if len(blocks) == 0 {
			delete(s.protection, key)
		}
	}
	return next
}
