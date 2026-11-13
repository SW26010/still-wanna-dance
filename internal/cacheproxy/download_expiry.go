package cacheproxy

import (
	"time"

	"still-wanna-dance/internal/blocksched"
)

// The source collection handles block deadline feedback. It ends only GETs
// whose remaining span and own clock have expired, then independently applies
// queue expiry policy. Resource execution follows the resulting usage plan.
func (s *Server) expireDownload(f *flight, key string, block int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.refreshUseSourcesLocked(now)
	var ended []*downloadDemand
	for d := range s.demands {
		if d.playback && d.flight == f && d.useSpan().contains(key, block) && d.clock != nil && blocksched.Expired(d.clock.ready(block*s.cfg.BlockSize), now) {
			d.exit.recordReason(withdrawalDownloadExpired)
			d.cancel(blocksched.ErrExpired)
			ended = append(ended, d)
		}
	}
	// Freeze the whole failed cohort before any removal retimes the queue.
	// Otherwise map order could preserve the end clock of the last failed GET.
	for _, d := range ended {
		s.removeDemandLocked(d)
	}
	// GET withdrawal can genuinely change queue timing. Judge the remaining
	// queue against that updated clock before ending its own preparation.
	now = time.Now()
	for _, q := range s.prefetchSources {
		if !q.use.contains(key, block) || !blocksched.Expired(q.readyBy(block, s.cfg.BlockSize), now) {
			continue
		}
		q.expired = true
		s.releaseUseLocked(q.use, now)
		q.use = useSpan{}
		for d := range s.demands {
			if d.queueSource == q {
				d.exit.recordReason(withdrawalNeedEnded)
				d.cancel(blocksched.ErrExpired)
				s.removeDemandLocked(d)
			}
		}
		s.notifyUseChangedLocked(key)
	}
}
