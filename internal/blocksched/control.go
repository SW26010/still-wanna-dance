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
	nextEstimate         time.Time
	estimated            int
	discard              bool
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
func (s *state) hasBlocking() bool { return s.blocking > 0 }
func (s *state) sampleProgress(j *job, now time.Time) {
	if j.progress == nil {
		return
	}
	if size := j.progress.size.Load(); size > 0 && size != j.initialRemaining {
		j.initialRemaining = size
		j.remaining = max(0, size-j.sampled)
	}
	if j.progress.invalid.Load() {
		s.control.discard = true
		j.remaining = j.initialRemaining
		j.started, j.sampled = now, j.progress.bytes.Load()
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
	for j := range s.running {
		s.sampleProgress(j, now)
	}
	if !c.discard && c.workerSeconds > 0 && c.bytes > 0 {
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
			s.emit("backoff", "no_throughput_gain", nil)
		}
		c.previousRate, c.previousConcurrency = throughput, concurrency
	}
	c.bytes, c.workerSeconds, c.discard = 0, 0, false
	c.nextSample = now.Add(sampleInterval)
}
func (s *state) estimate(now time.Time) int {
	count := 0
	var timed []*job
	for _, j := range s.jobs {
		if j.retiring || j.need.Owner == "" {
			continue
		}
		count++
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
				target = min(count, s.cfg.SoftLimit)
				break
			}
		}
	}
	if s.cfg.Adaptive && s.control.rate > 0 {
		sort.Slice(timed, func(i, j int) bool { return timed[i].need.ReadyBy.Before(timed[j].need.ReadyBy) })
		var bytes int64
		for _, j := range timed {
			bytes += j.remaining
			horizon := j.need.ReadyBy.Sub(now)
			if horizon <= 0 {
				horizon = catchupWindow
			}
			// A future deadline uses its actual remaining time. Bound the
			// estimate before conversion even when that time is almost zero.
			needed := math.Ceil(float64(bytes) / horizon.Seconds() / s.control.rate)
			target = max(target, int(min(float64(s.cfg.SoftLimit), needed)))
		}
	}
	if now.Before(s.control.coolUntil) {
		target = min(target, s.control.cap)
	}
	return min(count, s.cfg.SoftLimit, target)
}
func (s *state) regulate(now time.Time) {
	c := &s.control
	previousOpen, previousTarget := c.open, c.target
	defer func() {
		if c.open != previousOpen || c.target != previousTarget {
			s.emit("capacity", "demand_and_supply", nil)
		}
	}()
	if s.cfg.GrowInterval <= 0 {
		c.target = s.cfg.SoftLimit
	} else {
		if s.active > 0 && (c.nextSample.IsZero() || !now.Before(c.nextSample)) {
			s.measure(now)
		}
		// Aggregate cost only on the bounded control cadence. Demand updates
		// touch their block and heaps; they never sort every task.
		if s.wanted == 0 {
			c.estimated = 0
			c.nextEstimate = time.Time{}
		} else if c.nextEstimate.IsZero() || !now.Before(c.nextEstimate) {
			c.estimated = s.estimate(now)
			c.nextEstimate = now.Add(sampleInterval)
		}
		c.target = min(s.wanted, s.cfg.SoftLimit, max(c.estimated, min(2, s.wanted)))
		if now.Before(c.coolUntil) {
			c.target = min(c.target, c.cap)
		}
		if c.target == 0 {
			c.open = 1
			c.nextGrow = time.Time{}
			s.contention = time.Time{}
			if s.active == 0 {
				c.nextSample = time.Time{}
				c.previousConcurrency = 0
				c.previousRate = 0
				c.bytes, c.workerSeconds, c.discard = 0, 0, false
			}
			return
		}
		if c.target < c.open {
			c.open = c.target
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
	var victimRemaining int64
	blocking := s.hasBlocking()
	var due *job
	if len(s.ready) > 0 && !s.ready[0].need.ReadyBy.IsZero() && !s.ready[0].need.ReadyBy.After(now.Add(catchupWindow)) {
		due = s.ready[0]
	}
	for j := range s.running {
		// Contention can fire before the next 500 ms supply sample. Inspect
		// useful progress now, including response size discovery and retries.
		remaining, received := j.remaining, int64(0)
		if p := j.progress; p != nil {
			size := p.size.Load()
			if size == 0 {
				size = j.initialRemaining
			}
			received = p.received.Load()
			remaining = max(0, size-received)
		}
		if j.cancel != nil && !j.need.Blocking && !j.retiring && !j.yielding && (remaining > 64<<10 || received == 0) && (blocking || due != nil && needBefore(due.need, j.need)) {
			if victim == nil || needBefore(victim.need, j.need) || !needBefore(j.need, victim.need) && remaining > victimRemaining {
				victim = j
				victimRemaining = remaining
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
		victim.yielding = true
		victim.cancel(ErrPaused)
		reason := "deadline_contention"
		if blocking {
			reason = "blocking_contention"
		}
		s.emit("yield_requested", reason, victim)
		s.contention = now.Add(s.cfg.PreemptAfter)
	}
}
func (s *state) nextControlEvent(now time.Time) time.Time {
	next := earlier(earlier(s.control.nextGrow, s.contention), s.control.nextEstimate)
	if first := s.orphans.Front(); first != nil {
		next = earlier(next, first.Value.(*job).orphanUntil)
	}
	if s.active > 0 && s.cfg.GrowInterval > 0 {
		next = earlier(next, s.control.nextSample)
	}
	if !s.hasBlocking() && len(s.sleeping) > 0 {
		next = earlier(next, s.sleeping[0].notBefore)
	}
	if len(s.ready) > 0 && !s.ready[0].need.ReadyBy.IsZero() {
		at := s.ready[0].need.ReadyBy.Add(-catchupWindow)
		if at.After(now) {
			next = earlier(next, at)
		}
	}
	return next
}
