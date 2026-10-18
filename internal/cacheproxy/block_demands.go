package cacheproxy

import (
	"log/slog"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// Sources belong to the demand manager, not the scheduling core. Queue
// promotion/demotion keeps the same source identity and updates its attributes.
type demandSource uint8

const (
	sourceGET demandSource = iota + 1
	sourceQueueHead
	sourceQueue
	sourceBatch
)

type withdrawalReason uint8

const (
	withdrawalSourceGone withdrawalReason = iota
	withdrawalNeedEnded
	withdrawalCompleted
	withdrawalShutdown
)

const maxBlockContributions = 128

// Membership in this collection is Source=handoff; original source is only
// needed at conversion time and is not retained in the runtime record.
type handoffContribution struct {
	need blocksched.Need
}

func (w *downloadSession) logDemand(block int64, j *scheduledBlock) {
	if w.f.log == nil || !w.f.log.Enabled(w.ctx, slog.LevelDebug) {
		return
	}
	var get, head, queue, batch int
	for _, p := range j.needs {
		switch p.source {
		case sourceGET:
			get++
		case sourceQueueHead:
			head++
		case sourceQueue:
			queue++
		case sourceBatch:
			batch++
		}
	}
	w.f.log.Debug("block_demand", "block", block, "get_sources", get, "head_sources", head, "queue_sources", queue, "batch_sources", batch,
		"handoff_sources", len(j.handoffs), "handoff_suppressed", get > 0 && len(j.handoffs) > 0,
		"blocking", j.published.Blocking, "opportunistic", j.published.Opportunistic, "ready_by", j.published.ReadyBy, "lease_until", j.published.LeaseUntil, "next_expiry", j.nextExpiry)
}

func makeHandoff(old plannedNeed, reason withdrawalReason, now time.Time) (handoffContribution, bool) {
	if reason != withdrawalSourceGone || !old.need.LeaseUntil.After(now) {
		return handoffContribution{}, false
	}
	var grace, delay time.Duration
	switch old.source {
	case sourceGET:
		grace, delay = getHandoff, 2*time.Second
	case sourceQueueHead:
		grace, delay = queueHandoff, 5*time.Second
	default:
		return handoffContribution{}, false
	}
	return handoffContribution{need: handoffNeed(old.need, now, grace, delay)}, true
}

// aggregate preserves independent validity while filtering scheduling fields:
// any live GET suppresses handoff urgency, but does not delete/extend its lease.
// The resource timer wakes at the earliest expiry to recompute this summary;
// the scheduler's lease is a final lifetime bound if the manager stops updating.
func (j *scheduledBlock) aggregate(now time.Time) blocksched.Need {
	var n blocksched.Need
	j.nextExpiry, j.handoffUntil = time.Time{}, time.Time{}
	get := false
	for _, p := range j.needs {
		if p.source == sourceGET && p.need.LeaseUntil.After(now) {
			get = true
		}
	}
	include := func(need blocksched.Need, urgency bool) {
		if !need.LeaseUntil.After(now) {
			return
		}
		n.Owner = need.Owner
		n.LeaseUntil = maxTime(n.LeaseUntil, need.LeaseUntil)
		if j.nextExpiry.IsZero() || need.LeaseUntil.Before(j.nextExpiry) {
			j.nextExpiry = need.LeaseUntil
		}
		if urgency {
			n.Blocking = n.Blocking || need.Blocking
			n.Opportunistic = n.Opportunistic || need.Opportunistic || need.ReadyBy.IsZero()
			if !need.ReadyBy.IsZero() && (n.ReadyBy.IsZero() || need.ReadyBy.Before(n.ReadyBy)) {
				n.ReadyBy = need.ReadyBy
			}
		}
	}
	for _, p := range j.needs {
		include(p.need, true)
	}
	live := j.handoffs[:0]
	for _, h := range j.handoffs {
		if !h.need.LeaseUntil.After(now) {
			continue
		}
		live = append(live, h)
		include(h.need, !get)
		j.handoffUntil = maxTime(j.handoffUntil, h.need.LeaseUntil)
	}
	clear(j.handoffs[len(live):])
	j.handoffs = live
	return n
}

func (s demandSource) String() string {
	switch s {
	case sourceGET:
		return "GET"
	case sourceQueueHead:
		return "queue_head"
	case sourceQueue:
		return "queue"
	case sourceBatch:
		return "batch"
	default:
		return "unknown"
	}
}
