package blocksched

import (
	"container/heap"
	"time"
)

type rollingJob struct {
	job *job
	RollingDeadline
	index int
}

// A shared origin bakes in a stable ordering by offset. During real source
// changes different origins can coexist, so compare their heads at dispatch.
type rollingHeap []*rollingJob

func (h rollingHeap) Len() int { return len(h) }
func (h rollingHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.Offset != b.Offset {
		return a.Offset < b.Offset
	}
	return fairBefore(a.job, b.job)
}
func (h rollingHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *rollingHeap) Push(v any) {
	entry := v.(*rollingJob)
	entry.index = len(*h)
	*h = append(*h, entry)
}
func (h *rollingHeap) Pop() any {
	last := len(*h) - 1
	entry := (*h)[last]
	(*h)[last] = nil
	*h = (*h)[:last]
	entry.index = -1
	return entry
}

func (j *job) priorityAt(now time.Time) priority {
	p := j.need
	for _, entry := range j.rolling {
		p.ReadyBy = earlier(p.ReadyBy, entry.At(now))
	}
	return p
}

func (s *state) dequeueRolling(entry *rollingJob) {
	if entry.index >= 0 {
		h := s.rolling[entry.Base]
		heap.Remove(h, entry.index)
		if h.Len() == 0 {
			delete(s.rolling, entry.Base)
		}
	}
}

func (s *state) updateRolling(j *job, offsets map[time.Time]time.Duration, fairChanged bool) bool {
	changed := false
	for base, entry := range j.rolling {
		if _, ok := offsets[base]; !ok {
			s.dequeueRolling(entry)
			delete(j.rolling, base)
			changed = true
		}
	}
	for base, offset := range offsets {
		entry := j.rolling[base]
		if entry == nil {
			entry = &rollingJob{job: j, RollingDeadline: RollingDeadline{Base: base}, index: -1}
			if j.rolling == nil {
				j.rolling = make(map[time.Time]*rollingJob)
			}
			j.rolling[base] = entry
			changed = true
		}
		offsetChanged := entry.Offset != offset
		changed = changed || offsetChanged
		entry.Offset = offset
		if j.retiring || j.cancel != nil || j.need.Blocking || j.need.WindowClosed || entry.WindowAt(s.cfg.PrepareAhead).IsZero() {
			s.dequeueRolling(entry)
			continue
		}
		if entry.index >= 0 {
			if offsetChanged || fairChanged {
				heap.Fix(s.rolling[base], entry.index)
			}
			continue
		}
		h := s.rolling[base]
		if h == nil {
			h = new(rollingHeap)
			if s.rolling == nil {
				s.rolling = make(map[time.Time]*rollingHeap)
			}
			s.rolling[base] = h
		}
		heap.Push(h, entry)
	}
	return changed
}
