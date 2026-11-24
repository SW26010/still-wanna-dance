package upstreamstate

import (
	"context"
	"errors"
	"time"
)

type selectedCheck struct {
	done    chan struct{}
	catalog *catalogRound
}

type CheckKind string

const (
	CheckCatalog    CheckKind = "catalog"
	CheckPlayback   CheckKind = "playback"
	CheckLatency    CheckKind = "latency"
	CheckThroughput CheckKind = "throughput"
	// CheckConnectivity checks every operation, using one-byte resource probes.
	CheckConnectivity CheckKind = "connectivity"
)

func ValidCheckKind(kind CheckKind) bool {
	return kind == CheckCatalog || kind == CheckPlayback || kind == CheckLatency || kind == CheckThroughput || kind == CheckConnectivity
}

// CheckSelected preempts automatic work and reserves the next batch for the
// requested operation. Prerequisite lookups do not replace unrelated results.
// Cancellation stops and drains the reserved work, leaving the Monitor reusable.
func (m *Monitor) CheckSelected(ctx context.Context, kind CheckKind) (err error) {
	if !ValidCheckKind(kind) {
		return errors.New("unknown monitor check kind")
	}
	if m.experimentProbe == nil && kind.includes(Resource) {
		return errors.New("resource checks require the block sampler")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("monitor closed")
	}
	if m.manual != nil || (m.active != nil && m.batchManual) {
		m.mu.Unlock()
		return errors.New("manual check already running")
	}
	revision := m.refreshChannelLocked().Revision
	reserved := &selectedCheck{done: make(chan struct{})}
	if round := m.catalogRound; round != nil && round.requested && !round.done && round.revision == revision {
		// Transfer the pending result along with its readers. The retiring
		// worker can no longer publish or finish this catalog phase.
		reserved.catalog, m.catalogRound = round, nil
	} else if m.needsCatalogLocked(kind) {
		reserved.catalog = newCatalogRound(revision)
	}
	m.manual = reserved
	if m.active != nil {
		m.batchCancel()
	}
	m.mu.Unlock()
	defer func() {
		// checkRequest can return as soon as its caller is canceled. Keep the
		// reservation until the batch has released requests and callbacks.
		m.mu.Lock()
		done := m.active
		m.mu.Unlock()
		if done != nil {
			<-done
		}
		m.mu.Lock()
		// The caller may cancel the reservation before its batch starts.
		if reserved.catalog != nil {
			reserved.catalog.finish(err)
		}
		close(reserved.done)
		m.manual = nil
		m.mu.Unlock()
	}()
	return m.checkRequest(ctx, true, kind)
}

func (m *Monitor) needsCatalogLocked(kind CheckKind) bool {
	return kind.includes(Catalog) || m.songID == 0 || !time.Now().Before(m.songAt.Add(m.policy.SampleLifetime))
}

func (k CheckKind) includes(op Operation) bool {
	return k == "" || k == CheckConnectivity || (k == CheckCatalog && op == Catalog) || (k == CheckPlayback && op == PlaybackURL) || ((k == CheckLatency || k == CheckThroughput) && op == Resource)
}
