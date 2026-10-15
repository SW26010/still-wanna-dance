package blocksched

import "time"

const defaultPrepareAhead = 2 * time.Second

type controller struct {
	open     int
	nextGrow time.Time
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// The timed heap contains Blocking and finite deadlines. Untimed background
// work can use capacity while its head is outside the activation window.
func (s *state) nextReady(now time.Time) *job {
	if s.ready.Len() > 0 {
		j := s.ready.first()
		if j.need.Blocking || !s.windowAt(j).After(now) {
			return j
		}
	}
	if s.untimed.Len() > 0 {
		return s.untimed.first()
	}
	return nil
}

func (s *state) windowAt(j *job) time.Time {
	return j.need.ReadyBy.Add(-s.durations.value()).Add(-s.cfg.PrepareAhead)
}

// Dispatch has already reused available slots. Grow only while eligible work
// still waits for capacity; running work never asks for another full block T.
func (s *state) regulate(now time.Time) {
	c := &s.control
	before := c.open
	defer func() {
		if c.open != before {
			s.emit("capacity", "activation_window", nil)
		}
	}()
	if s.cfg.GrowInterval <= 0 {
		return
	}
	if s.active == 0 {
		c.open = 1
	}
	if s.nextReady(now) == nil || s.active < c.open || c.open >= s.cfg.SoftLimit {
		c.nextGrow = time.Time{}
		return
	}
	if c.nextGrow.IsZero() {
		c.nextGrow = now.Add(s.cfg.GrowInterval)
	} else if !now.Before(c.nextGrow) {
		c.open++
		s.dispatch(now)
		c.nextGrow = time.Time{}
		if s.nextReady(now) != nil && s.active >= c.open && c.open < s.cfg.SoftLimit {
			c.nextGrow = now.Add(s.cfg.GrowInterval)
		}
	}
}

func (s *state) nextControlEvent(now time.Time) time.Time {
	next := s.control.nextGrow
	if s.ready.Len() > 0 && !s.ready.first().need.Blocking {
		if at := s.windowAt(s.ready.first()); at.After(now) {
			next = earlier(next, at)
		}
	}
	if first := s.orphans.Front(); first != nil {
		next = earlier(next, first.Value.(*job).orphanUntil)
	}
	return next
}
