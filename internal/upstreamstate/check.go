package upstreamstate

import (
	"context"
	"net/http"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
)

// checkRoutes owns candidate discovery and the catalog -> playback -> resource
// flow. Separate monitors own separate configuration and observation state.
func (m *Monitor) checkRoutes(ctx context.Context, p Policy, source upstreamrequest.Snapshot, record func(observation), checkCatalog bool) {
	kind := m.batchKind
	started := time.Now()
	provider := source.Candidates
	publish := func(o observation, c upstreamrequest.Candidate) {
		o.channel = c.ID
		m.mu.Lock()
		defer m.mu.Unlock()
		record(o)
	}
	var preparedMu sync.Mutex
	prepared := make(map[string]bool)
	prepare := func(target string) []upstreamrequest.Candidate {
		qctx, cancel := context.WithTimeout(ctx, p.RequestTimeout)
		defer cancel()
		cs, err := provider.Candidates(qctx, target)
		ready := err == nil && qctx.Err() == nil
		preparedMu.Lock()
		prepared[target] = ready
		preparedMu.Unlock()
		return cs
	}

	var ids []int64
	var catalogs []CatalogResponse
	for _, route := range routeIDs(Catalog) {
		if !checkCatalog || ctx.Err() != nil {
			break
		}
		cs := prepare(entry(Catalog, route))
		samples := make([]int64, len(cs))
		responses := make([]CatalogResponse, len(cs))
		m.sampleRoutes(ctx, Catalog, route, cs, p.RequestTimeout, func(probeCtx context.Context, i int) bool {
			c := cs[i]
			r, id, response := upstreamcheck.ReadValidatedCatalog(probeCtx, []*http.Client{requestClient(c.Transport)}, probePolicy(p), route)
			if probeCtx.Err() != nil {
				return false
			}
			o := observed(r)
			o.channel = c.ID
			m.mu.Lock()
			record(o)
			if round := m.catalogRound; round != nil {
				round.observe(r, id, response)
			}
			m.mu.Unlock()
			// Sync consumes the cached parse error as well as valid catalogs.
			responses[i], samples[i] = response, id
			return r.State == "available"
		})
		ids = append(ids, samples...)
		for _, response := range responses {
			if len(response.Body) > 0 {
				catalogs = append(catalogs, response)
			}
		}
	}

	m.mu.Lock()
	if round := m.catalogRound; round != nil {
		round.finish(ctx.Err())
	}
	if m.refreshChannelLocked().Revision != source.Revision {
		m.mu.Unlock()
		return
	}
	for _, id := range ids {
		if id > 0 {
			m.songID, m.songAt = id, time.Now()
			break
		}
	}
	if !time.Now().Before(m.songAt.Add(p.SampleLifetime)) {
		m.songID = 0
	}
	id := m.songID
	m.mu.Unlock()
	// A demand-only check delivers its result to ReadCatalog; its caller owns sync.
	if !m.catalogReadOnly() {
		m.deliverCatalogs(ctx, source.Revision, catalogs)
	}
	if m.playbackSongCandidates != nil && ctx.Err() == nil && kind != CheckCatalog {
		for _, sample := range m.playbackSongCandidates(ctx) {
			if sample > 0 {
				id = sample
				break
			}
		}
	}
	if id > 0 && kind != CheckCatalog && ctx.Err() == nil {
		m.publishProgress(ctx, PlaybackURL)
		playback := func(samples chan<- videoSample) {
			var routes sync.WaitGroup
			for _, r := range playbackRoutes {
				routes.Add(1)
				go func(r route) {
					defer routes.Done()
					cs := prepare(entry(PlaybackURL, r.ID))
					m.sampleRoutes(ctx, PlaybackURL, r.ID, cs, p.RequestTimeout, func(probeCtx context.Context, i int) bool {
						c := cs[i]
						o, sample, response := probePlaybackResponse(probeCtx, requestClient(c.Transport), p, id, r)
						if probeCtx.Err() != nil {
							return false
						}
						publish(o, c)
						m.deliverPlaybackURL(ctx, source.Revision, response)
						if samples != nil && sample != nil {
							select {
							case samples <- *sample:
							case <-ctx.Done():
							}
						}
						return o.state == "available"
					})
				}(r)
			}
			routes.Wait()
		}
		if m.experimentProbe != nil && m.batchManual && kind.includes(Resource) {
			m.experimentProbe(ctx, p, id, kind, playback, prepare, publish)
		} else {
			playback(nil)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshChannelLocked().Revision == source.Revision {
		m.updatePreferencesLocked(provider, started, kind)
		valid := make(map[string]bool)
		for _, op := range operations {
			for _, route := range m.routeIDsLocked(op) {
				for _, c := range provider.Current(entry(op, route)) {
					valid[string(op)+"/"+route+"/"+c.ID] = true
				}
			}
		}
		for key := range m.history {
			if !valid[key] {
				delete(m.history, key)
			}
		}
		for key := range m.diagnostics {
			if !valid[key] {
				delete(m.diagnostics, key)
			}
		}
		for key := range m.resourceSources {
			if !valid[key] {
				m.removeResourceSourceLocked(key)
			}
		}
		// Restored samples need not have any entry in history. Only prune
		// confirmed candidate sets. Failed discovery and unqueried routes wait.
		if ctx.Err() == nil {
			m.pruneThroughputLocked(valid, prepared)
		}
	}
}
