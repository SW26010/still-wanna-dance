package cacheproxy

import (
	"time"

	"still-wanna-dance/internal/blocksched"
)

// A deadline ends the old flight through the ordinary shared error path. The
// source collection owns the resulting withdrawals, not the scheduler. Manual
// preparations and later queue clocks survive and may retry after flight cleanup.
func (s *Server) expireDownload(f *flight, key string, block int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshUseSourcesLocked(time.Now())
	var ended []*downloadDemand
	for d := range s.demands {
		if d.playback && d.key == key && d.flight == f && d.firstBlock < d.lastBlock {
			d.exit.recordReason(withdrawalNeedEnded)
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
	now := time.Now()
	for _, q := range s.prefetchSources {
		if !q.use.contains(key, block) || !blocksched.Expired(q.readyBy(block), now) {
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
