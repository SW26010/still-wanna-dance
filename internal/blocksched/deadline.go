package blocksched

import (
	"container/heap"
	"time"
)

// DownloadGrace is the finite investment after the requested ready time. It
// does not restart at admission or at any HTTP retry within an execution.
const DownloadGrace = 30 * time.Second

func Deadline(readyBy time.Time) time.Time {
	if readyBy.IsZero() {
		return time.Time{}
	}
	return readyBy.Add(DownloadGrace)
}

func Expired(readyBy, now time.Time) bool {
	return !readyBy.IsZero() && !Deadline(readyBy).After(now)
}

// One indexed heap covers queued and executing tasks. Updates come from actual
// demand changes; expired tasks are retired before a command can update them.
func (s *state) updateDeadline(j *job) {
	if j.retiring || j.need.Owner == "" || j.need.ReadyBy.IsZero() {
		s.removeDeadline(j)
	} else if j.deadlineIndex < 0 {
		heap.Push(&s.deadlines, j)
	} else {
		heap.Fix(&s.deadlines, j.deadlineIndex)
	}
}

func (s *state) removeDeadline(j *job) {
	if j.deadlineIndex >= 0 {
		heap.Remove(&s.deadlines, j.deadlineIndex)
	}
}

func (s *state) expireDeadlines(now time.Time) {
	for len(s.deadlines) > 0 && Expired(s.deadlines[0].need.ReadyBy, now) {
		j := s.deadlines[0]
		s.emit("retired", "download_deadline", j)
		s.retire(j, ErrExpired)
	}
}

type deadlineHeap []*job

func (h deadlineHeap) Len() int           { return len(h) }
func (h deadlineHeap) Less(i, j int) bool { return h[i].need.ReadyBy.Before(h[j].need.ReadyBy) }
func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].deadlineIndex, h[j].deadlineIndex = i, j
}
func (h *deadlineHeap) Push(value any) {
	j := value.(*job)
	j.deadlineIndex = len(*h)
	*h = append(*h, j)
}
func (h *deadlineHeap) Pop() any {
	a := *h
	j := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	j.deadlineIndex = -1
	return j
}
