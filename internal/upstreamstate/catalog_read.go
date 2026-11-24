package upstreamstate

import (
	"context"
	"net/http"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

// Foreground retries share one caller deadline, independent of probe budgets.
const catalogReadPaths = 3

// One catalog phase owns this response. Closing ready publishes an immutable
// result to all cold readers, before slower probes or storage callbacks finish.
type catalogRound struct {
	revision  uint64
	ready     chan struct{}
	requested bool
	done      bool
	result    upstreamcheck.Result
	id        int64
	response  CatalogResponse
}

func newCatalogRound(revision uint64) *catalogRound {
	return &catalogRound{revision: revision, ready: make(chan struct{})}
}

// Called with the monitor lock held.
func (r *catalogRound) observe(result upstreamcheck.Result, id int64, response CatalogResponse) {
	if r.done {
		return
	}
	// A later remote failure without a body cannot explain the rejected fields.
	if len(r.response.Body) > 0 && len(response.Body) == 0 && !localObservation(result.State) {
		return
	}
	r.result, r.id, r.response = result, id, response
	if result.State == "available" {
		r.finish(nil)
	}
}

func (r *catalogRound) finish(err error) {
	if !r.done {
		if r.result.State == "" {
			if err == nil {
				err = upstreamrequest.ErrUnavailable
			}
			r.result = upstreamcheck.CatalogFailure("api", err)
		}
		r.done = true
		close(r.ready)
	}
}

// ReadCatalog reads the HTTPS primary through measured paths. A reader with
// no recent success joins a catalog check; only the monitor owns its probes.
func (m *Monitor) ReadCatalog(ctx context.Context) (upstreamcheck.Result, int64, CatalogResponse) {
	failure := func(err error) (upstreamcheck.Result, int64, CatalogResponse) {
		return upstreamcheck.CatalogFailure("api", err), 0, CatalogResponse{}
	}
	for {
		if err := ctx.Err(); err != nil {
			return failure(err)
		}
		m.mu.Lock()
		source := m.refreshChannelLocked()
		if m.closed || source.Candidates == nil {
			m.mu.Unlock()
			return failure(upstreamrequest.ErrUnavailable)
		}
		paths := m.selectionsLocked(source, Catalog, Constraints{})
		if len(paths) > 0 {
			m.mu.Unlock()
			var clients []*http.Client
			for _, path := range paths[:min(len(paths), catalogReadPaths)] {
				clients = append(clients, requestClient(path.Channel.Transport))
			}
			return upstreamcheck.ReadValidatedCatalog(ctx, clients, probePolicy(m.policy), "api")
		}
		if m.active == nil && m.manual == nil {
			kind := CheckCatalog
			if m.scheduler != nil && !time.Now().Before(m.nextCheck) {
				kind = ""
			}
			m.startBatchLocked(m.ctx, source, false, kind)
		}
		round := m.catalogRound
		wait := m.active
		if m.manual != nil && (m.active == nil || !m.batchManual) {
			round = m.manual.catalog
		}
		if round != nil && round.revision == source.Revision {
			round.requested = true
			wait = round.ready
		} else {
			round = nil
			if wait == nil && m.manual != nil {
				wait = m.manual.done
			}
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return failure(ctx.Err())
		case <-m.ctx.Done():
			return failure(m.ctx.Err())
		case <-source.Changed:
			return failure(upstreamrequest.ErrRetired)
		case <-wait:
			if m.channel.Snapshot().Revision != source.Revision {
				return failure(upstreamrequest.ErrRetired)
			}
			if round != nil {
				if err := ctx.Err(); err != nil {
					return failure(err)
				}
				return round.result, round.id, round.response
			}
		}
	}
}
