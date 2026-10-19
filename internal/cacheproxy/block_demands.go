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
	sourceHandoff
)

type withdrawalReason uint8

const (
	withdrawalSourceGone withdrawalReason = iota
	withdrawalNeedEnded
)

// blockUse is the source-free contract between demand collection and resource
// use. Protection includes all finite usage; download urgency alone filters an
// old handoff while a live GET exists. Actual reads are outside this contract.
type blockUse struct {
	need         blocksched.Need
	nextExpiry   time.Time
	protectReady time.Time
}

// Completed work becomes eligible for automatic refetch only with a current
// finite download need entering protection. Local grace alone is insufficient.
func (u blockUse) refetchAt(ahead time.Duration) time.Time {
	if u.need.ReadyBy.IsZero() {
		return time.Time{}
	}
	return u.protectReady.Add(-ahead)
}

type blockSource struct {
	kind string
	id   any
}
type plannedNeed struct {
	need   blocksched.Need
	source demandSource
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

func mergeUse(a, b blockUse) blockUse {
	if b.need.Owner != "" {
		a.need.Owner = b.need.Owner
	}
	a.need.ReadyBy = earlier(a.need.ReadyBy, b.need.ReadyBy)
	a.need.LeaseUntil = maxTime(a.need.LeaseUntil, b.need.LeaseUntil)
	a.need.Opportunistic = a.need.Opportunistic || b.need.Opportunistic
	a.nextExpiry = earlier(a.nextExpiry, b.nextExpiry)
	a.protectReady = earlier(a.protectReady, b.protectReady)
	return a
}

// aggregateUse belongs to demand collection. Original identities never cross
// this boundary, and no download task holds or expires source contributions.
func aggregateUse(plan map[blockSource]plannedNeed, now time.Time) blockUse {
	var result blockUse
	get := false
	for _, p := range plan {
		if p.source == sourceGET && p.need.LeaseUntil.After(now) {
			get = true
		}
	}
	for _, p := range plan {
		// The registry still owns a handoff until its fixed expiry is processed.
		// Preserve its protection clock even when delivery of that event is late.
		result.protectReady = earlier(result.protectReady, p.need.ReadyBy)
		if !p.need.LeaseUntil.After(now) {
			continue
		}
		if p.source == sourceHandoff && blocksched.Expired(p.need.ReadyBy, now) {
			continue // Usage/protection lasts to its fixed exit, but cannot fund another expired download.
		}
		n := p.need
		if p.source == sourceHandoff && get {
			n.ReadyBy = time.Time{}
			n.Opportunistic = false
		} else {
			n.Opportunistic = n.Opportunistic || n.ReadyBy.IsZero()
		}
		result = mergeUse(result, blockUse{need: n, nextExpiry: n.LeaseUntil})
	}
	return result
}

func (w *downloadSession) logDemand(block int64, j *scheduledBlock) {
	if w.f.log == nil || !w.f.log.Enabled(w.ctx, slog.LevelDebug) {
		return
	}
	w.f.log.Debug("block_demand", "block", block,
		"opportunistic", j.published.Opportunistic, "ready_by", j.published.ReadyBy,
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
	case sourceBatch:
		return "batch"
	default:
		return "unknown"
	}
}
