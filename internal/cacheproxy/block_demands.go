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
)

type withdrawalReason uint8

const (
	withdrawalSourceGone withdrawalReason = iota
	withdrawalNeedEnded
	withdrawalDownloadExpired
)

func (r withdrawalReason) String() string {
	switch r {
	case withdrawalSourceGone:
		return "source_gone"
	case withdrawalDownloadExpired:
		return "download_deadline"
	default:
		return "need_ended"
	}
}

// blockUse is the source-free contract between demand collection and resource
// use. Closed contributions retain their own clock without changing open
// sources' admission or deadline. Actual reads are outside this contract.
type blockUse struct {
	need       blocksched.Need
	nextExpiry time.Time
	protectAt  time.Time
}

// Completed work becomes eligible for automatic refetch only with a current
// finite download need entering protection. Local grace alone is insufficient.
func (u blockUse) refetchAt() time.Time {
	if u.need.WindowClosed || u.need.EarliestReady().IsZero() {
		return time.Time{}
	}
	return u.protectAt
}

type blockSource struct {
	kind string
	id   any
}
type plannedNeed struct {
	need blocksched.Need
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func mergeUse(a, b blockUse) blockUse {
	if b.need != (blocksched.Need{}) {
		// Closed sources contribute their clock only until an open source exists.
		if a.need == (blocksched.Need{}) || a.need.WindowClosed && !b.need.WindowClosed {
			a.need.ReadyBy = b.need.ReadyBy
			a.need.Rolling = b.need.Rolling
			a.need.WindowClosed = b.need.WindowClosed
		} else if a.need.WindowClosed == b.need.WindowClosed {
			a.need.ReadyBy = earlier(a.need.ReadyBy, b.need.ReadyBy)
			// Queue contributions share the same origin within this plan.
			if d := b.need.Rolling; !d.Base.IsZero() && (a.need.Rolling.Base.IsZero() || d.Offset < a.need.Rolling.Offset) {
				a.need.Rolling = d
			}
		}
	}
	// Early admission and its order are independent of the finite deadline.
	// Inactive/timed-only sources must not contribute the default zero order.
	if b.need.Opportunistic && !b.need.WindowClosed {
		if !a.need.Opportunistic || b.need.OpportunisticOrder < a.need.OpportunisticOrder {
			a.need.OpportunisticOrder = b.need.OpportunisticOrder
		}
		a.need.Opportunistic = true
	}
	a.need.LeaseUntil = maxTime(a.need.LeaseUntil, b.need.LeaseUntil)
	a.nextExpiry = earlier(a.nextExpiry, b.nextExpiry)
	a.protectAt = earlier(a.protectAt, b.protectAt)
	return a
}

func (p plannedNeed) use(now time.Time, ahead time.Duration) blockUse {
	// The registry retains local protection until it processes withdrawal,
	// including a fixed handoff whose ordinary download window is closed.
	result := blockUse{protectAt: p.need.WindowAt(ahead)}
	if !p.need.LeaseUntil.After(now) || sourceExpired(p.need.ReadyBy, now) {
		return result // Registered usage can still protect local data.
	}
	result.need = p.need
	result.need.Opportunistic = !p.need.WindowClosed && (p.need.Opportunistic || p.need.EarliestReady().IsZero())
	result.nextExpiry = p.need.LeaseUntil
	if p.need.WindowClosed {
		result.nextExpiry = earlier(result.nextExpiry, sourceDeadline(p.need.ReadyBy))
	}
	return result
}

func (w *downloadSession) logDemand(block int64, j *scheduledBlock) {
	if w.f.log == nil || !w.f.log.Enabled(w.ctx, slog.LevelDebug) {
		return
	}
	w.f.log.Debug("块需求已更新", "event_name", "block_demand", "block", block,
		"opportunistic", j.published.Opportunistic, "opportunistic_order", j.published.OpportunisticOrder,
		"window_closed", j.published.WindowClosed, "ready_by", j.published.ReadyAt(time.Now()),
		"lease_until", j.published.LeaseUntil, "next_expiry", j.nextExpiry)
}

func (s demandSource) String() string {
	switch s {
	case sourceGET:
		return "GET"
	case sourceQueueHead:
		return "queue_head"
	case sourceQueue:
		return "queue"
	default:
		return "unknown"
	}
}
