package blocksched

import (
	"math"
	"sort"
	"time"
)

const sampleInterval = 500 * time.Millisecond
const catchupWindow = 2 * time.Second

type controller struct {
	open, target         int
	nextGrow, nextSample time.Time
	bytes                int64
	workerSeconds        float64
	rate, previousRate   float64
	previousConcurrency  float64
	cap                  int
	coolUntil            time.Time
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}
func (s *state) hasBlocking() bool {
	for _, j := range s.jobs {
		if j.need.Owner != "" && j.need.Blocking && !j.retiring {
			return true
		}
	}
	return false
}
func (s *state) sampleProgress(j *job, now time.Time) {
	if j.progress == nil {
		return
	}
	n := j.progress.bytes.Load()
	s.control.bytes += max(0, n-j.sampled)
	s.control.workerSeconds += now.Sub(j.started).Seconds()
	j.remaining = max(0, j.remaining-max(0, n-j.sampled))
	j.started, j.sampled = now, n
}
func (s *state) measure(now time.Time) {
	c := &s.control
	for _, j := range s.jobs {
		if j.cancel != nil {
			s.sampleProgress(j, now)
		}
	}
	if c.workerSeconds > 0 && c.bytes > 0 {
		perWorker := float64(c.bytes) / c.workerSeconds
		throughput := float64(c.bytes) / sampleInterval.Seconds()
		concurrency := c.workerSeconds / sampleInterval.Seconds()
		if c.rate == 0 {
			c.rate = perWorker
		} else {
			c.rate = 0.75*c.rate + 0.25*perWorker
		}
		// Only compare busy windows. Queued/paused work contributes no worker
		// time, and a drained window never diagnoses the network as slow.
		if concurrency > c.previousConcurrency+0.75 && c.previousConcurrency >= 1 && throughput < c.previousRate*1.10 {
			c.cap = max(1, int(math.Ceil(c.previousConcurrency)))
			c.coolUntil = now.Add(5 * time.Second)
		}
		c.previousRate, c.previousConcurrency = throughput, concurrency
	}
	c.bytes, c.workerSeconds = 0, 0
	c.nextSample = now.Add(sampleInterval)
}
func (s *state) estimate(now time.Time) int {
	count, blocking := 0, 0
	var timed []*job
	for _, j := range s.jobs {
		if j.retiring || j.need.Owner == "" {
			continue
		}
		count++
		if j.need.Blocking {
			blocking++
		}
		if !j.need.ReadyBy.IsZero() {
			timed = append(timed, j)
		}
	}
	if count == 0 {
		return 0
	}
	// A single cautious probe is useful even before measured supply exists.
	target := min(2, count)
	if s.control.rate == 0 {
		for _, j := range timed {
			if j.need.ReadyBy.Before(now.Add(catchupWindow)) {
				target = min(count, s.cfg.Background)
				break
			}
		}
	}
	if s.cfg.Adaptive && s.control.rate > 0 {
		sort.Slice(timed, func(i, j int) bool { return timed[i].need.ReadyBy.Before(timed[j].need.ReadyBy) })
		var bytes int64
		for _, j := range timed {
			bytes += j.remaining
			horizon := max(catchupWindow, j.need.ReadyBy.Sub(now)).Seconds()
			target = max(target, int(math.Ceil(float64(bytes)/horizon/s.control.rate)))
		}
	}
	if now.Before(s.control.coolUntil) {
		target = min(target, s.control.cap)
	}
	// A finite emergency allowance protects two independent blocked reads.
	target = max(target, min(blocking, 2))
	ceiling := s.cfg.Background
	if blocking > 0 {
		ceiling = s.cfg.Limit
	}
	return min(count, ceiling, target)
}
func (s *state) regulate(now time.Time) {
	c := &s.control
	if s.cfg.GrowInterval <= 0 {
		c.target = s.cfg.Limit
	} else {
		if s.active > 0 && (c.nextSample.IsZero() || !now.Before(c.nextSample)) {
			s.measure(now)
		}
		c.target = s.estimate(now)
		if c.target == 0 {
			c.open = 1
			c.nextGrow = time.Time{}
			if s.active == 0 {
				c.nextSample = time.Time{}
				c.previousConcurrency = 0
				c.previousRate = 0
			}
			return
		}
		if c.target < c.open {
			c.open = c.target
		}
		if s.hasBlocking() {
			c.open = max(c.open, min(2, c.target))
		}
		if c.open < c.target {
			if c.nextGrow.IsZero() {
				c.nextGrow = now.Add(s.cfg.GrowInterval)
			} else if !now.Before(c.nextGrow) {
				c.open++
				c.nextGrow = now.Add(s.cfg.GrowInterval)
			}
		} else {
			c.nextGrow = time.Time{}
		}
	}
	if s.cfg.PreemptAfter <= 0 {
		return
	}
	// A free request slot does not remove bandwidth contention. Give existing
	// low-urgency transfers a bounded chance to finish before yielding one.
	var victim *job
	blocking := s.hasBlocking()
	var due *job
	if len(s.ready) > 0 && !s.ready[0].need.ReadyBy.IsZero() && !s.ready[0].need.ReadyBy.After(now.Add(catchupWindow)) {
		due = s.ready[0]
	}
	for _, j := range s.jobs {
		if j.cancel != nil && !j.need.Blocking && !j.retiring && j.remaining > 64<<10 && (blocking || due != nil && needBefore(due.need, j.need)) {
			if victim == nil || needBefore(victim.need, j.need) || !needBefore(j.need, victim.need) && j.remaining > victim.remaining {
				victim = j
			}
		}
	}
	if (!blocking && due == nil) || victim == nil {
		s.contention = time.Time{}
		return
	}
	if s.contention.IsZero() {
		s.contention = now.Add(s.cfg.PreemptAfter)
	}
	if !now.Before(s.contention) {
		victim.cancel(ErrPaused)
		s.emit("yield_requested", "blocking_contention", victim)
		s.contention = now.Add(s.cfg.PreemptAfter)
	}
}
func (s *state) nextControlEvent(now time.Time) time.Time {
	next := earlier(s.control.nextGrow, s.contention)
	if s.active > 0 && s.cfg.GrowInterval > 0 {
		next = earlier(next, s.control.nextSample)
	}
	if !s.hasBlocking() {
		for j := range s.delayed {
			if j.notBefore.After(now) {
				next = earlier(next, j.notBefore)
			}
		}
	}
	if len(s.ready) > 0 && !s.ready[0].need.ReadyBy.IsZero() {
		at := s.ready[0].need.ReadyBy.Add(-catchupWindow)
		if at.After(now) {
			next = earlier(next, at)
		}
	}
	return next
}
