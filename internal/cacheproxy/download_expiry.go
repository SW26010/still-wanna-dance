package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const sourceGrace = 30 * time.Second

var errSourceExpired = fmt.Errorf("download need-by window expired: %w", context.DeadlineExceeded)

func sourceDeadline(readyBy time.Time) time.Time {
	if readyBy.IsZero() {
		return time.Time{}
	}
	return readyBy.Add(sourceGrace)
}

func sourceExpired(readyBy, now time.Time) bool {
	return !readyBy.IsZero() && !sourceDeadline(readyBy).After(now)
}

func (s *Server) missingDeadline(span useSpan, clock playbackClock, firstMissing func(useSpan) (int64, bool)) time.Time {
	if block, ok := firstMissing(span); ok {
		return sourceDeadline(clock.ready(block * s.cfg.BlockSize))
	}
	return time.Time{}
}

// A failed flight identifies a GET waiting for a replacement address.
// Another flight may since have stored blocks or published the whole resource.
func (s *Server) failedGETDeadlineLocked(d *downloadDemand) time.Time {
	if d.flight != nil && d.clock != nil {
		select {
		case <-d.flight.done:
			if errors.Is(d.flight.err, errUpstreamDownload) {
				return s.missingDeadline(d.useSpan(), *d.clock, s.firstMissingLocal)
			}
		default:
		}
	}
	return time.Time{}
}

func (d *downloadDemand) expireGET() {
	d.exit.recordReason(withdrawalDownloadExpired)
	d.cancel(errSourceExpired)
}

// While the old flight is gone, the address lookup can outlive the GET. Wake
// this source at its first missing block's deadline, even without a new flight.
// The returned function settles the same deadline when the lookup finishes.
func (s *Server) watchRetryDeadline(d *downloadDemand) func() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	check := func() time.Time {
		if !s.demands[d] || d.flight == nil || d.ctx.Err() != nil || s.closed {
			return time.Time{}
		}
		next := s.refreshGETDeadlinesLocked(d.key, time.Now(), s.failedGETDeadlineLocked)
		if d.ctx.Err() != nil {
			return time.Time{}
		}
		return next
	}
	var timer *time.Timer
	if at := check(); !at.IsZero() {
		timer = time.AfterFunc(time.Until(at), func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if timer != nil {
				if next := check(); !next.IsZero() {
					timer.Reset(time.Until(next))
				}
			}
		})
	}
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		check()
		return context.Cause(d.ctx)
	}
}

func (s *Server) refreshDownloadDeadlinesLocked(f *flight, key string, now time.Time, firstMissing func(useSpan) (int64, bool)) time.Time {
	return s.refreshGETDeadlinesLocked(key, now, func(d *downloadDemand) time.Time {
		switch d.flight {
		case nil:
			return s.missingDeadline(d.useSpan(), *d.clock, s.firstMissingLocal)
		case f:
			return s.missingDeadline(d.useSpan(), *d.clock, firstMissing)
		default:
			return s.failedGETDeadlineLocked(d)
		}
	})
}

func (s *Server) refreshGETDeadlinesLocked(key string, now time.Time, deadline func(*downloadDemand) time.Time) time.Time {
	s.refreshUseSourcesLocked(now)
	var next time.Time
	var ended []*downloadDemand
	for d := range s.demands {
		if !d.playback || d.key != key || d.clock == nil {
			continue
		}
		at := deadline(d)
		if at.After(now) {
			next = earlier(next, at)
		} else if !at.IsZero() && d.flight != nil { // Attachment determines the GET outcome.
			d.expireGET()
			ended = append(ended, d)
		}
	}
	// Freeze the whole failed cohort before publishing any of its withdrawals.
	for _, d := range ended {
		s.removeDemandLocked(d)
	}
	return next
}
