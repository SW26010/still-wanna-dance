package cacheproxy

import (
	"errors"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
)

// Keep only execution metadata, not a source, storage reference or download
// slot. Availability and the current source-free usage projection decide when
// another shared flight is needed after preparation has completed.
type resourceUse struct {
	video   video
	refetch bool // The last flight permits another attempt; terminal failures do not.
}

func (s *Server) continueResourceUseLocked(v video, f *flight) {
	refetch := f.err == nil || (f.ranges == nil || !f.ranges.sequential) &&
		(errors.Is(f.err, ErrNotRetained) || errors.Is(f.err, errPreempted) || errors.Is(f.err, blocksched.ErrExpired))
	if use := s.resourceUses[v.key]; use != nil {
		use.refetch = refetch
		return
	}
	if !refetch || f.err != nil && !errors.Is(f.err, ErrNotRetained) {
		return
	}
	s.startResourceUseLocked(v)
}

// Local hits and completed flights retain the same coordination, without
// extending the source or keeping a file pinned. Caller holds mu.
func (s *Server) startResourceUseLocked(v video) {
	if s.closed || s.resourceUses[v.key] != nil {
		return
	}
	if s.resourceUses == nil {
		s.resourceUses = make(map[string]*resourceUse)
	}
	u := &resourceUse{video: resourceVideo(v), refetch: true}
	u.video.songID, u.video.localOnly = v.songID, v.localOnly
	s.resourceUses[v.key] = u
	s.wg.Add(1)
	go s.coordinateResourceUse(u)
}

// Both Range and sequential downloads call this after a successful complete
// block write. Observation is notified regardless of the reclamation policy.
func (s *Server) notifyBlockStored() {
	s.notifyBlockObservation()
	s.requestBlockReclamation()
}

// Availability changes only wake affected resources, without changing usage
// or protection. Call after releasing retentionMu to preserve lock ordering.
func (s *Server) notifyResourceAvailability(keys ...string) {
	s.notifyBlockObservation()
	s.mu.Lock()
	for _, key := range keys {
		s.notifyResourceChangedLocked(key)
	}
	s.mu.Unlock()
}

func (s *Server) coordinateResourceUse(u *resourceUse) {
	defer s.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	s.mu.Lock()
	changes := s.watchResourceLocked(u.video.key)
	defer func() {
		s.unwatchResourceLocked(u.video.key)
		delete(s.resourceUses, u.video.key)
		s.mu.Unlock()
	}()
	for !s.closed && s.storageFailure == nil {
		now := time.Now()
		plan, _ := s.usePlanLocked(u.video.key, now)
		if len(plan) == 0 {
			return
		}
		var done <-chan struct{}
		var next time.Time
		if f := s.flights[u.video.key]; f != nil {
			done = f.done // Existing owners and reentries always share one flight.
		} else {
			// A failed attempt has already exhausted normal retry/fallback.
			// Do not turn continuing usage into an unbounded retry loop.
			if !u.refetch {
				return
			}
			next = s.nextResourceRefetchLocked(u.video, plan)
			if !next.IsZero() && !next.After(now) {
				log := s.cfg.Logger.With("trace_id", applog.TraceID(applog.WithTrace(s.ctx)), "resource_key", u.video.key, "background", true)
				f, err := s.startFlightLocked(u.video, true, log)
				if err != nil {
					log.Error("cache_task_failed", "error", applog.SafeError(err))
					return
				}
				done, next = f.done, time.Time{}
			}
		}
		changed := changes.changed
		timer.Stop()
		var entered <-chan time.Time
		if !next.IsZero() {
			timer.Reset(max(0, time.Until(next)))
			entered = timer.C
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
		case <-changed:
		case <-done:
		case <-entered:
		}
		s.mu.Lock()
	}
}

// Caller holds mu. As in a running session's completed-work filter, only a
// current finite usage entering protection can fund automatic refetch. A local
// grace tail or an untimed source alone must not cause refill/evict cycles.
func (s *Server) nextResourceRefetchLocked(v video, plan map[int64]blockUse) time.Time {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if _, retained := s.retained[v.key]; retained {
		return time.Time{}
	}
	local := s.localResources[v.key]
	var next time.Time
	for b, use := range plan {
		if b < 0 || b >= blockCount(v.size) || local != nil && local.has(b) {
			continue
		}
		next = earlier(next, use.refetchAt(s.protectionAhead()))
	}
	return next
}
