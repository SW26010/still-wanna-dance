package blocksched

import (
	"context"
	"time"
)

// Keep an idle task briefly for reattachment, including a Submit without Add.
// This is only a metadata lifetime; no demand means no execution permission.
const orphanRetention = 30 * time.Second

func (s *state) retainOrphan(j *job) {
	if j.orphan != nil {
		return // Cleanup and duplicate submissions must not extend retention.
	}
	j.orphanUntil = time.Now().Add(orphanRetention)
	// A fixed duration makes insertion order also expiry order. The list gives
	// constant-time removal on reattachment, with no stale timer entries.
	j.orphan = s.orphans.PushBack(j)
}

func (s *state) removeOrphan(j *job) {
	if j.orphan != nil {
		s.orphans.Remove(j.orphan)
		j.orphan = nil
		j.orphanUntil = time.Time{}
	}
}

func (s *state) expireOrphans(now time.Time) {
	for first := s.orphans.Front(); first != nil; first = s.orphans.Front() {
		j := first.Value.(*job)
		if now.Before(j.orphanUntil) {
			return
		}
		s.emit("retired", "no_demand", j)
		// Retiring rejects new demands immediately. Any in-flight cleanup must
		// finish before Done closes and the task/execution capacity is released.
		s.retire(j, context.Canceled)
	}
}
